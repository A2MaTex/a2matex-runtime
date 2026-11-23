"""I1 real-agent read acceptance for a retained synthetic execution; no Android."""
import json
import os
from pathlib import Path
import socket
import sqlite3
import sys
import urllib.error
import urllib.request
from uuid import uuid4

sys.dont_write_bytecode = True
ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(ROOT / 'internal/discovery/testdata'))
from managed_parent import launch, readiness, probe


def verify(binary, root, device):
    env = dict(os.environ, XDG_STATE_HOME=str(root), XDG_RUNTIME_DIR=str(root), LOCALAPPDATA=str(root),
               A2MATEX_RUNTIME_ADB_EXECUTABLE_PATH=str(root / 'adb-not-installed'))
    with socket.socket() as sock:
        sock.bind(('127.0.0.1', 0))
        port = sock.getsockname()[1]
    expected = json.loads((root / 'expected.json').read_text())
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    database = next(root.rglob('execution.sqlite3'))

    def records():
        with sqlite3.connect(database.as_uri() + '?mode=ro', uri=True) as db:
            db.execute('BEGIN')
            return [db.execute('SELECT * FROM ' + name).fetchall() for name in
                    ('run_groups', 'device_executions', 'node_attempts', 'execution_events', 'event_stream_state')]

    before = records()
    tokens = []
    for _ in range(2):
        process, commands, ready = launch(binary, env, port)
        try:
            connection = readiness(ready)
            assert connection['sessionToken'] not in tokens
            tokens.append(connection['sessionToken'])

            def get(suffix, status=200, auth=True):
                probe(connection)
                headers = {'Authorization': 'Bearer ' + connection['sessionToken']} if auth else {}
                request = urllib.request.Request(connection['apiBaseUrl'] + suffix, headers=headers)
                try:
                    response = opener.open(request, timeout=3)
                except urllib.error.HTTPError as error:
                    response = error
                with response:
                    body = json.load(response)
                    assert response.status == status, (response.status, body)
                    assert response.headers['Cache-Control'] == 'no-store'
                    return body

            path = '/device-executions/' + device + '/attempts'
            assert get(path) == expected
            for i in range(3):
                page = get(path + f'?afterSequence={i}&limit=1')
                assert page['attempts'] == expected['attempts'][i:i + 1]
                assert page['nextAfterSequence'] == (i + 1 if i < 2 else None)
            assert get(path + '?afterSequence=3') == {'attempts': [], 'nextAfterSequence': None}
            get(path + '?limit=0', 400)
            get(path + '?limit=1&limit=2', 400)
            get('/device-executions/' + str(uuid4()) + '/attempts', 404)
            get(path, 401, auth=False)
            assert records() == before, 'history/startup reads changed durable execution data'
            commands.write(b'shutdown\n')
            assert process.wait(10) == 0 and ready.read(1) == b''
        finally:
            if process.poll() is None:
                process.kill()
                process.wait(10)
            commands.close()
            ready.close()
        assert records() == before, 'idle shutdown rewrote retained results'
    print('I1 actual-agent acceptance PASS: retained history, paging, auth/errors, two sessions and no execution writes/Android', flush=True)


if __name__ == '__main__':
    verify(str(Path(sys.argv[1]).resolve(strict=True)), Path(sys.argv[2]).resolve(strict=True), sys.argv[3])
