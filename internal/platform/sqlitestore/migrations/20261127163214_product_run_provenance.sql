-- +goose Up
ALTER TABLE run_groups ADD COLUMN execution_kind TEXT NOT NULL DEFAULT 'DEVELOPMENT'
 CHECK (execution_kind IN ('DEVELOPMENT','PRODUCT'));
CREATE TABLE product_run_provenance (
 run_group_id TEXT PRIMARY KEY NOT NULL REFERENCES run_groups(id) ON DELETE RESTRICT,
 product_id TEXT NOT NULL,
 release_id TEXT NOT NULL,
 release_version TEXT NOT NULL,
 package_hash TEXT NOT NULL,
 runtime_ir_hash TEXT NOT NULL,
 package_root_hash TEXT NOT NULL,
 runtime_session_id TEXT NOT NULL,
 lease_family_id TEXT NOT NULL,
 lease_sequence INTEGER NOT NULL CHECK (lease_sequence > 0),
 refresh_after INTEGER NOT NULL,
 expires_at INTEGER NOT NULL CHECK (expires_at > refresh_after)
) STRICT;

-- +goose Down
-- Fail rather than expose retained Product data as Development after downgrade.
CREATE TABLE product_downgrade_guard (count INTEGER CHECK (count = 0));
INSERT INTO product_downgrade_guard SELECT count(*) FROM run_groups WHERE execution_kind = 'PRODUCT';
DROP TABLE product_downgrade_guard;
DROP TABLE product_run_provenance;
ALTER TABLE run_groups DROP COLUMN execution_kind;
