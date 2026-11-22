"""H4 actual-agent acceptance; controlled ADB is not real Android acceptance."""
import copy
import json
import os
from pathlib import Path
import socket
import sqlite3
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request
import uuid

ROOT = Path(__file__).resolve().parents[3]
sys.dont_write_bytecode = True
sys.path.insert(0, str(ROOT / 'internal/discovery/testdata'))
from managed_parent import launch, readiness, probe


def verify(binary):
    with tempfile.TemporaryDirectory() as directory:
        root = Path(directory)
        adb = root / 'adb'
        adb.write_text(f'#!{sys.executable}\n' + '''import sys
args = sys.argv[1:]
if args == ['version']:
    print('Android Debug Bridge version 1.0.41')
elif args == ['devices', '-l']:
    print('List of devices attached')
    for i in reversed(range(40)):
        print(f'device-{i}\\tdevice')
    print('offline-device\\toffline')
elif len(args) == 6 and args[0] == '-s' and args[2:] == ['shell', '-T', 'getprop', 'sys.boot_completed']:
    print('1')
else:
    sys.exit(2)
''')
        adb.chmod(0o700)
        env = dict(os.environ, XDG_STATE_HOME=directory, XDG_RUNTIME_DIR=directory,
                   A2MATEX_RUNTIME_ADB_EXECUTABLE_PATH=str(adb))
        with socket.socket() as sock:
            sock.bind(('127.0.0.1', 0))
            port = sock.getsockname()[1]
        children = []
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))

        def start():
            child = launch(binary, env, port)
            children.append(child)
            return child[0], child[1], readiness(child[2])

        process, command, connection = start()

        def request(method, path, body=None, expected=200):
            probe(connection)
            wire = None if body is None else json.dumps(body).encode()
            req = urllib.request.Request(connection['apiBaseUrl'] + path, data=wire, method=method,
                headers={'Authorization': 'Bearer ' + connection['sessionToken'], 'Content-Type': 'application/json'})
            try:
                response = opener.open(req, timeout=15)
            except urllib.error.HTTPError as error:
                response = error
            with response:
                data = json.load(response)
                assert response.code == expected, (response.code, data)
                return data

        def database(query, args=()):
            dbfile = next(root.rglob('execution.sqlite3'))
            with sqlite3.connect(dbfile.as_uri() + '?mode=ro', uri=True) as db:
                return db.execute(query, args).fetchall()

        def counts():
            return database('SELECT (SELECT count(*) FROM run_groups), (SELECT count(*) FROM device_executions), '
                            '(SELECT count(*) FROM node_attempts), (SELECT count(*) FROM execution_events)')[0]

        def wait_group(group_id):
            until = time.monotonic() + 15
            while time.monotonic() < until:
                snapshot = request('GET', '/runs/' + group_id)
                if snapshot['runGroup']['status'] in ('SUCCEEDED', 'FAILED', 'CANCELLED', 'INTERRUPTED'):
                    return snapshot
                time.sleep(0.02)
            raise AssertionError('group did not settle')

        template = json.loads((ROOT / 'examples/h4/start-run.json').read_text())

        def body(count=2, duration=0):
            value = copy.deepcopy(template)
            value['requestId'] = str(uuid.uuid4())
            value['devices'] = [{'adbSerial': f'device-{i}', 'inputs': {'delayMs': duration}} for i in range(count)]
            return value

        try:
            compiled = request('POST', '/workflow-compilation', json.loads(
                (ROOT / 'examples/h4/compile-workflow.json').read_text()))
            assert compiled['validation']['status'] in ('VALID', 'VALID_WITH_WARNINGS'), compiled
            # Complete example IR must also be accepted; the first run uses it directly.
            first = body(duration=3000)
            wire = json.dumps(first).encode()
            with socket.create_connection(('127.0.0.1', port), timeout=10) as sock:
                header = (f'POST /api/v1/runs HTTP/1.1\r\nHost: 127.0.0.1\r\n'
                    f'Authorization: Bearer {connection["sessionToken"]}\r\nContent-Type: application/json\r\n'
                    f'Content-Length: {len(wire)}\r\nConnection: close\r\n\r\n').encode()
                sock.sendall(header + wire)
                with sock.makefile('rb') as received:
                    assert received.readline().startswith(b'HTTP/1.1 201'), 'new admission failed'
                    # Lose the response body after admission; resolve with the retained identity.
            saved = request('POST', '/runs', first)
            group_id = saved['runGroup']['id']
            assert [d['adbSerial'] for d in saved['deviceExecutions']] == ['device-0', 'device-1']
            request('POST', '/runs', body(), 409)
            finished = wait_group(group_id)
            assert finished['runGroup']['summary']['succeeded'] == 2
            assert counts() == (1, 2, 6, 24), counts()
            before = counts()
            request('POST', '/runs', first)
            assert counts() == before, 'terminal replay wrote or redispatched'

            for mode in ('empty', 'duplicate', 'later input', 'missing', 'offline'):
                invalid = body()
                expected = 400 if mode in ('empty', 'duplicate') else 422
                if mode == 'empty': invalid['devices'] = []
                if mode == 'duplicate': invalid['devices'][1]['adbSerial'] = 'device-0'
                if mode == 'later input': invalid['devices'][1]['inputs'] = {'delayMs': 'invalid'}
                if mode == 'missing': invalid['devices'][1]['adbSerial'] = 'missing-device'
                if mode == 'offline': invalid['devices'][1]['adbSerial'] = 'offline-device'
                rejection = request('POST', '/runs', invalid, expected)
                if mode in ('missing', 'offline'):
                    issue = rejection['unavailableDevices'][0]
                    assert issue['index'] == 1 and issue['adbSerial'] == invalid['devices'][1]['adbSerial']
                assert counts() == before, (mode, counts())

            large = body(33)
            large['runtimeIr'] = compiled['runtimeIr']
            created = request('POST', '/runs', large, 201)
            assert wait_group(created['runGroup']['id'])['runGroup']['summary']['succeeded'] == 33
            assert counts() == (2, 35, 105, 389), counts()

            stopped = request('POST', '/runs', body(duration=5000), 201)
            stop_id = stopped['runGroup']['id']
            command.write(b'shutdown\n')
            assert process.wait(10) == 0
            assert database('SELECT status FROM run_groups WHERE id=?', (stop_id,)) == [('CANCELLED',)]
            assert database('SELECT count(*) FROM device_executions WHERE run_group_id=? AND status=?',
                            (stop_id, 'CANCELLED')) == [(2,)]
            before = counts()
            process, command, connection = start()
            assert request('GET', '/runs/' + stop_id)['runGroup']['status'] == 'CANCELLED'
            assert counts() == before, 'restart changed settled work'
            command.write(b'shutdown\n')
            assert process.wait(10) == 0
            print('H4 actual-agent HTTP/SQLite acceptance passed: compile, 2/33 devices, lost-response replay, '
                  'whole-selection rejection, ordered diagnostics, shutdown and restart without redispatch.')
        finally:
            for child, commands, ready in children:
                if child.poll() is None:
                    child.kill()
                    child.wait(10)
                commands.close()
                ready.close()


if __name__ == '__main__':
    verify(str(Path(sys.argv[1]).resolve(strict=True)))
