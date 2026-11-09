-- +goose Up
CREATE TABLE run_groups (
    id TEXT PRIMARY KEY NOT NULL,
    request_id TEXT NOT NULL UNIQUE,
    status TEXT NOT NULL CHECK (status IN ('RUNNING','PAUSED','SUCCEEDED','FAILED','CANCELLED','INTERRUPTED')),
    created_at INTEGER NOT NULL,
    finished_at INTEGER,
    CHECK ((status IN ('RUNNING','PAUSED')) = (finished_at IS NULL)),
    CHECK (finished_at IS NULL OR finished_at >= created_at)
) STRICT;

CREATE UNIQUE INDEX run_groups_one_active ON run_groups ((1))
    WHERE status IN ('RUNNING','PAUSED');

CREATE TABLE device_executions (
    id TEXT PRIMARY KEY NOT NULL,
    run_group_id TEXT NOT NULL REFERENCES run_groups(id) ON DELETE RESTRICT,
    adb_serial TEXT NOT NULL,
    admission_ordinal INTEGER NOT NULL CHECK (admission_ordinal >= 0),
    status TEXT NOT NULL CHECK (status IN ('QUEUED','RUNNING','PAUSED','SUCCEEDED','FAILED','CANCELLED','INTERRUPTED')),
    next_node_id TEXT,
    created_at INTEGER NOT NULL,
    started_at INTEGER,
    finished_at INTEGER,
    terminal_error_json TEXT,
    UNIQUE (run_group_id, adb_serial),
    UNIQUE (run_group_id, admission_ordinal),
    CHECK ((status IN ('QUEUED','RUNNING','PAUSED')) = (finished_at IS NULL)),
    CHECK ((finished_at IS NULL) = (next_node_id IS NOT NULL)),
    CHECK (started_at IS NULL OR started_at >= created_at),
    CHECK (finished_at IS NULL OR finished_at >= coalesce(started_at, created_at)),
    CHECK (terminal_error_json IS NULL OR
        (status IN ('FAILED','INTERRUPTED') AND json_valid(terminal_error_json) AND json_type(terminal_error_json) = 'object'))
) STRICT;

CREATE TABLE node_attempts (
    id TEXT PRIMARY KEY NOT NULL,
    device_execution_id TEXT NOT NULL REFERENCES device_executions(id) ON DELETE RESTRICT,
    sequence INTEGER NOT NULL CHECK (sequence > 0),
    node_id TEXT NOT NULL,
    attempt_number INTEGER NOT NULL CHECK (attempt_number BETWEEN 1 AND 3),
    status TEXT NOT NULL CHECK (status IN ('RUNNING','SUCCEEDED','FAILED','TIMED_OUT','CANCELLED','INTERRUPTED')),
    started_at INTEGER NOT NULL,
    finished_at INTEGER,
    output_json TEXT,
    error_json TEXT,
    UNIQUE (device_execution_id, sequence),
    UNIQUE (device_execution_id, node_id, attempt_number),
    CHECK ((status = 'RUNNING') = (finished_at IS NULL)),
    CHECK (finished_at IS NULL OR finished_at >= started_at),
    CHECK ((status = 'SUCCEEDED') = (output_json IS NOT NULL)),
    CHECK ((status IN ('FAILED','TIMED_OUT','INTERRUPTED')) = (error_json IS NOT NULL)),
    CHECK (output_json IS NULL OR (json_valid(output_json) AND json_type(output_json) = 'object')),
    CHECK (error_json IS NULL OR (json_valid(error_json) AND json_type(error_json) = 'object'))
) STRICT;

CREATE UNIQUE INDEX node_attempts_one_running ON node_attempts (device_execution_id)
    WHERE status = 'RUNNING';

CREATE TABLE execution_events (
    cursor INTEGER PRIMARY KEY CHECK (cursor > 0),
    event_type TEXT NOT NULL CHECK (event_type IN ('RUN_GROUP_STATE_CHANGED','DEVICE_EXECUTION_STATE_CHANGED','NODE_ATTEMPT_STATE_CHANGED','NODE_RETRY_SCHEDULED')),
    run_group_id TEXT NOT NULL REFERENCES run_groups(id) ON DELETE RESTRICT,
    device_execution_id TEXT REFERENCES device_executions(id) ON DELETE RESTRICT,
    node_attempt_id TEXT REFERENCES node_attempts(id) ON DELETE RESTRICT,
    node_id TEXT,
    occurred_at INTEGER NOT NULL,
    payload_json TEXT NOT NULL CHECK (json_valid(payload_json) AND json_type(payload_json) = 'object'),
    CHECK ((event_type = 'RUN_GROUP_STATE_CHANGED') = (device_execution_id IS NULL)),
    CHECK ((event_type IN ('NODE_ATTEMPT_STATE_CHANGED','NODE_RETRY_SCHEDULED')) = (node_attempt_id IS NOT NULL)),
    CHECK ((node_attempt_id IS NULL) = (node_id IS NULL))
) STRICT;

CREATE TABLE event_stream_state (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    last_committed_cursor INTEGER NOT NULL,
    replay_floor INTEGER NOT NULL CHECK (replay_floor >= 0 AND replay_floor <= last_committed_cursor)
) STRICT;

INSERT INTO event_stream_state (id, last_committed_cursor, replay_floor) VALUES (1, 0, 0);


-- +goose Down
DROP TABLE IF EXISTS event_stream_state;
DROP TABLE IF EXISTS execution_events;
DROP TABLE IF EXISTS node_attempts;
DROP TABLE IF EXISTS device_executions;
DROP TABLE IF EXISTS run_groups;