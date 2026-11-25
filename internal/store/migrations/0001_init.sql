CREATE TABLE meta (
    key   TEXT PRIMARY KEY,
    value BLOB NOT NULL
);

CREATE TABLE connections (
    id              TEXT PRIMARY KEY,
    name            TEXT NOT NULL,
    config_url      TEXT NOT NULL,
    auth_kind       TEXT NOT NULL,
    status          TEXT NOT NULL,
    app_id          INTEGER NOT NULL DEFAULT 0,
    app_slug        TEXT NOT NULL DEFAULT '',
    app_client_id   TEXT NOT NULL DEFAULT '',
    app_html_url    TEXT NOT NULL DEFAULT '',
    installation_id INTEGER NOT NULL DEFAULT 0,
    secret          BLOB NOT NULL,
    created_at      INTEGER NOT NULL
);

-- RESTRICT: removing a connection that still has pools must fail, the controller owns scale
-- sets on GitHub that would otherwise leak.
CREATE TABLE pools (
    id            TEXT PRIMARY KEY,
    connection_id TEXT NOT NULL REFERENCES connections(id) ON DELETE RESTRICT,
    name          TEXT NOT NULL,
    vcpus         INTEGER NOT NULL,
    memory_mb     INTEGER NOT NULL,
    arch          TEXT NOT NULL DEFAULT '',
    max_runners   INTEGER NOT NULL,
    cluster       TEXT NOT NULL DEFAULT '',
    node          TEXT NOT NULL DEFAULT '',
    runner_group  TEXT NOT NULL DEFAULT '',
    image         TEXT NOT NULL DEFAULT '',
    enabled       INTEGER NOT NULL DEFAULT 0,
    scale_set_id  INTEGER NOT NULL DEFAULT 0,
    last_error    TEXT NOT NULL DEFAULT '',
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL,
    UNIQUE (connection_id, name)
);

-- Runners and jobs deliberately have no foreign key to pools: history and orphaned runners must
-- outlive a deleted pool so the controller can still clean up their Oakestra services.
CREATE TABLE runners (
    name            TEXT PRIMARY KEY,
    pool_id         TEXT NOT NULL,
    service_id      TEXT NOT NULL DEFAULT '',
    state           TEXT NOT NULL,
    oakestra_status TEXT NOT NULL DEFAULT '',
    cluster_id      TEXT NOT NULL DEFAULT '',
    worker_id       TEXT NOT NULL DEFAULT '',
    job_request_id  INTEGER NOT NULL DEFAULT 0,
    created_at      INTEGER NOT NULL,
    running_at      INTEGER,
    removed_at      INTEGER
);
CREATE INDEX runners_pool ON runners(pool_id);
CREATE INDEX runners_state ON runners(state);

-- seq is the paging key: every job has one, unlike started_at.
CREATE TABLE jobs (
    seq             INTEGER PRIMARY KEY AUTOINCREMENT,
    pool_id         TEXT NOT NULL,
    request_id      INTEGER NOT NULL,
    runner_name     TEXT NOT NULL DEFAULT '',
    repository      TEXT NOT NULL DEFAULT '',
    workflow_ref    TEXT NOT NULL DEFAULT '',
    job_name        TEXT NOT NULL DEFAULT '',
    workflow_run_id INTEGER NOT NULL DEFAULT 0,
    event_name      TEXT NOT NULL DEFAULT '',
    result          TEXT NOT NULL DEFAULT '',
    queued_at       INTEGER,
    started_at      INTEGER,
    finished_at     INTEGER,
    cluster_id      TEXT NOT NULL DEFAULT '',
    worker_id       TEXT NOT NULL DEFAULT '',
    UNIQUE (pool_id, request_id)
);
CREATE INDEX jobs_finished ON jobs(finished_at);

CREATE TABLE oakestra_credentials (
    id           INTEGER PRIMARY KEY CHECK (id = 1),
    username     TEXT NOT NULL,
    password     BLOB NOT NULL,
    organization TEXT NOT NULL DEFAULT ''
);

CREATE TABLE manifest_states (
    state      TEXT PRIMARY KEY,
    connection TEXT NOT NULL,
    secret     BLOB NOT NULL,
    created_at INTEGER NOT NULL
);
