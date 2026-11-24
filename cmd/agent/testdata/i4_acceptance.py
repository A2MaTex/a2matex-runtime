"""I4 real-agent SSE acceptance using I1 retained data and owner fixtures."""
import http.client
import json
import os
from pathlib import Path
import socket
import sqlite3
import sys
import time
import urllib.error
import urllib.request

sys.dont_write_bytecode = True
ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(ROOT / 'internal/discovery/testdata'))
from managed_parent import launch, readiness, probe


def verify(binary, root):
    env = dict(os.environ, XDG_STATE_HOME=str(root), XDG_RUNTIME_DIR=str(root),
               LOCALAPPDATA=str(root), A2MATEX_RUNTIME_ADB_EXECUTABLE_PATH=str(root / 'no-adb'))
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    expected = json.loads((root / 'expected-events.json').read_text())
    database = next(root.rglob('execution.sqlite3'))

    def records():
        with sqlite3.connect(database.as_uri() + '?mode=ro', uri=True) as db:
            return [db.execute('SELECT * FROM ' + table).fetchall() for table in
                    ('run_groups', 'device_executions', 'node_attempts', 'execution_events', 'event_stream_state')]

    before, tokens = records(), []
    for iteration in range(2):
        with socket.socket() as sock:
            sock.bind(('127.0.0.1', 0))
            port = sock.getsockname()[1]
        process, commands, ready = launch(binary, env, port)
        live = None
        try:
            connection = readiness(ready)
            assert connection['sessionToken'] not in tokens
            tokens.append(connection['sessionToken'])
            probe(connection)

            def request(cursor=None, accept=True, auth=True, status=200):
                headers = {}
                if auth:
                    headers['Authorization'] = 'Bearer ' + connection['sessionToken']
                if accept:
                    headers['Accept'] = 'text/event-stream'
                if cursor is not None:
                    headers['Last-Event-ID'] = str(cursor)
                req = urllib.request.Request(connection['apiBaseUrl'] + '/events', headers=headers)
                try:
                    response = opener.open(req, timeout=20)
                except urllib.error.HTTPError as error:
                    response = error
                assert response.status == status
                assert response.headers['Cache-Control'] == 'no-store'
                if status != 200:
                    with response:
                        body = json.load(response)
                        assert 'error' in body
                    return None
                assert response.headers['Content-Type'] == 'text/event-stream; charset=utf-8'
                assert response.headers['X-Accel-Buffering'] == 'no'
                assert response.headers.get('Content-Encoding') is None
                return response

            request(auth=False, status=401)
            request(accept=False, status=400)
            request(cursor=0, status=400)
            request(cursor=int(expected[-1]['cursor']) + 1, status=400)
            with request(cursor=1) as replay:
                for event in expected[1:]:
                    assert replay.readline() == ('id: ' + event['cursor'] + '\n').encode()
                    assert replay.readline() == ('event: ' + event['type'] + '\n').encode()
                    line = replay.readline()
                    assert line.startswith(b'data: ') and json.loads(line[6:]) == event
                    assert replay.readline() == b'\n'

            live = request()  # Headers arrive without waiting for data.
            if iteration == 0:
                started = time.monotonic()
                for _ in range(3):
                    assert live.readline() == b': heartbeat\n'
                    assert live.readline() == b'\n'
                assert time.monotonic() - started >= 40, 'stream did not survive the default 30-second write timeout'
                probe(connection)  # Other requests remain usable during the stream.
                commands.write(b'shutdown\n')
            else:
                commands.close()  # Owner loss, distinct from intentional shutdown.
            assert process.wait(10) == 0 and ready.read(1) == b''
            try:
                assert live.read(1) == b''
            except http.client.IncompleteRead as error:
                assert not error.partial
            assert records() == before, 'SSE or idle shutdown changed retained execution data'
        finally:
            if live is not None:
                live.close()
            if process.poll() is None:
                process.kill()
                process.wait(10)
            commands.close()
            ready.close()
    print('I4 actual-agent PASS: replay, auth/errors, three real heartbeats past 30s, fresh sessions, owner shutdown/loss and unchanged SQLite; no Android', flush=True)


if __name__ == '__main__':
    verify(str(Path(sys.argv[1]).resolve(strict=True)), Path(sys.argv[2]).resolve(strict=True))
