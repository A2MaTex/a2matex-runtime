"""Linux acceptance harness for the managed Runtime process contract."""

import concurrent.futures
import json
import os
from pathlib import Path
import select
import signal
import socket
import subprocess
import sys
import tempfile
import threading
import urllib.error
import urllib.request


def launch(binary, env, port):
    command_read, command_write = os.pipe()
    ready_read, ready_write = os.pipe()
    try:
        process = subprocess.Popen(
            [binary, "--owner-command-pipe", str(command_read),
             "--owner-ready-pipe", str(ready_write),
             "--listen-address", f"127.0.0.1:{port}"],
            env=env, pass_fds=(command_read, ready_write),
            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
        )
    except BaseException:
        os.close(command_write)
        os.close(ready_read)
        raise
    finally:
        os.close(command_read)
        os.close(ready_write)
    return process, os.fdopen(command_write, "wb", buffering=0), os.fdopen(ready_read, "rb")


def readiness(pipe):
    assert select.select([pipe], [], [], 10)[0], "readiness timed out"
    return json.loads(pipe.readline(1024))


def probe(connection, token=None):
    request = urllib.request.Request(
        connection["apiBaseUrl"] + "/runtime-info",
        headers={"Authorization": "Bearer " + (token or connection["sessionToken"])},
    )
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    with opener.open(request, timeout=0.5) as response:
        assert response.status == 200


def verify(binary):
    with tempfile.TemporaryDirectory() as directory:
        env = dict(os.environ, XDG_STATE_HOME=directory, XDG_RUNTIME_DIR=directory)
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            port = sock.getsockname()[1]

        children = []

        def start():
            child = launch(binary, env, port)
            children.append(child)
            return child

        try:
            process, command, ready = start()
            first = readiness(ready)
            probe(first)
            assert not list(Path(directory).rglob("runtime-connection.json"))

            competitor, _, rejected = start()
            assert competitor.wait(10) == 2
            assert rejected.read() == b""

            command.write(b"shutdown\n")
            assert process.wait(10) == 0 and ready.read() == b""

            process, command, ready = start()
            second = readiness(ready)
            assert second["sessionToken"] != first["sessionToken"]
            try:
                probe(second, first["sessionToken"])
            except urllib.error.HTTPError as exc:
                assert exc.code == 401
                exc.close()
            else:
                raise AssertionError("old token still accepted")

            command.close()
            assert process.wait(10) == 0 and ready.read() == b""

            process, _, ready = start()
            connection = readiness(ready)
            process.kill()
            process.wait(10)
            assert ready.read() == b""

            lock = threading.Lock()
            current = [process, connection]
            launch_count = len(children)

            def ensure_ready():
                with lock:
                    if current[0].poll() is None:
                        try:
                            probe(current[1])
                            return current[1]
                        except TimeoutError:
                            current[0].kill()
                            current[0].wait(10)
                    child, _, pipe = start()
                    current[:] = child, readiness(pipe)
                    probe(current[1])
                    return current[1]

            with concurrent.futures.ThreadPoolExecutor(max_workers=4) as executor:
                results = list(executor.map(lambda _: ensure_ready(), range(4)))
                assert all(result == results[0] for result in results)
                assert len(children) == launch_count + 1

                current[0].send_signal(signal.SIGSTOP)
                results = list(executor.map(lambda _: ensure_ready(), range(4)))
                assert all(result == results[0] for result in results)
                assert len(children) == launch_count + 2

            print("Managed launch, authentication, lock contention, shutdown, owner EOF,"
                  " external kill, and shared replacement after probe timeout passed.")
        finally:
            for child, command, ready in children:
                if child.poll() is None:
                    child.kill()
                    child.wait(10)
                command.close()
                ready.close()


if __name__ == "__main__":
    verify(str(Path(sys.argv[1]).resolve(strict=True)))
