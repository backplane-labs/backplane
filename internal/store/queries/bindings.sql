-- Versions of bindings and rules (internal/bindings).

-- name: NextBindingVersion :one
SELECT (COALESCE(max(version), 0) + 1)::bigint AS version
FROM backplane.binding_version WHERE hook = @hook;

-- name: InsertBindingVersion :one
INSERT INTO backplane.binding_version (hook, version, definition, author, comment, rollback_of, managed_by)
VALUES (@hook, @version, @definition, @author, @comment, @rollback_of, @managed_by)
RETURNING hook, version, definition, author, comment, created_at, rollback_of, managed_by;

-- name: SetBindingCurrent :exec
INSERT INTO backplane.binding_current (hook, version) VALUES (@hook, @version)
ON CONFLICT (hook) DO UPDATE SET version = EXCLUDED.version, updated_at = now();

-- name: GetBindingVersion :one
SELECT hook, version, definition, author, comment, created_at, rollback_of, managed_by
FROM backplane.binding_version WHERE hook = @hook AND version = @version;

-- name: GetCurrentBinding :one
SELECT v.hook, v.version, v.definition, v.author, v.comment, v.created_at, v.rollback_of, v.managed_by
FROM backplane.binding_current c
JOIN backplane.binding_version v ON v.hook = c.hook AND v.version = c.version
WHERE c.hook = @hook;

-- name: ListCurrentBindings :many
SELECT v.hook, v.version, v.definition, v.author, v.comment, v.created_at, v.rollback_of, v.managed_by
FROM backplane.binding_current c
JOIN backplane.binding_version v ON v.hook = c.hook AND v.version = c.version
ORDER BY v.hook;

-- name: ListBindingVersions :many
SELECT hook, version, definition, author, comment, created_at, rollback_of, managed_by
FROM backplane.binding_version
WHERE hook = @hook AND version < @before
ORDER BY version DESC
LIMIT @page_size;

-- name: CreateRule :one
INSERT INTO backplane.rule DEFAULT VALUES
RETURNING id, paused, created_at, updated_at;

-- name: EnsureRule :exec
-- Creates the rule of a wiring file under the id derived from its name.
INSERT INTO backplane.rule (id) VALUES (@id) ON CONFLICT (id) DO NOTHING;

-- name: GetRule :one
SELECT id, paused, created_at, updated_at FROM backplane.rule WHERE id = @id;

-- name: SetRulePaused :one
UPDATE backplane.rule SET paused = @paused, updated_at = now() WHERE id = @id
RETURNING id, paused, created_at, updated_at;

-- name: TouchRule :exec
UPDATE backplane.rule SET updated_at = now() WHERE id = @id;

-- name: NextRuleVersion :one
SELECT (COALESCE(max(version), 0) + 1)::bigint AS version
FROM backplane.rule_version WHERE rule_id = @rule_id;

-- name: InsertRuleVersion :one
INSERT INTO backplane.rule_version (rule_id, version, name, definition, author, comment, rollback_of, managed_by)
VALUES (@rule_id, @version, @name, @definition, @author, @comment, @rollback_of, @managed_by)
RETURNING rule_id, version, name, definition, author, comment, created_at, rollback_of, managed_by;

-- name: SetRuleCurrent :exec
INSERT INTO backplane.rule_current (rule_id, version) VALUES (@rule_id, @version)
ON CONFLICT (rule_id) DO UPDATE SET version = EXCLUDED.version, updated_at = now();

-- name: GetRuleVersion :one
SELECT rule_id, version, name, definition, author, comment, created_at, rollback_of, managed_by
FROM backplane.rule_version WHERE rule_id = @rule_id AND version = @version;

-- name: GetCurrentRule :one
SELECT r.id, r.paused, r.created_at, r.updated_at,
       v.version, v.name, v.definition, v.author, v.comment, v.created_at AS version_created_at, v.rollback_of, v.managed_by
FROM backplane.rule r
JOIN backplane.rule_current c ON c.rule_id = r.id
JOIN backplane.rule_version v ON v.rule_id = c.rule_id AND v.version = c.version
WHERE r.id = @id;

-- name: ListCurrentRules :many
SELECT r.id, r.paused, r.created_at, r.updated_at,
       v.version, v.name, v.definition, v.author, v.comment, v.created_at AS version_created_at, v.rollback_of, v.managed_by
FROM backplane.rule r
JOIN backplane.rule_current c ON c.rule_id = r.id
JOIN backplane.rule_version v ON v.rule_id = c.rule_id AND v.version = c.version
ORDER BY v.name, r.id;

-- name: ListRuleVersions :many
SELECT rule_id, version, name, definition, author, comment, created_at, rollback_of, managed_by
FROM backplane.rule_version
WHERE rule_id = @rule_id AND version < @before
ORDER BY version DESC
LIMIT @page_size;

-- name: BindingsStamp :one
-- A number that moves on every save: the sum of the versions in force
-- (each save raises one) plus the rows (a first save adds one). Read from
-- the small "current" tables, not the versions.
SELECT (SELECT COALESCE(sum(version), 0) + count(*) FROM backplane.binding_current)::bigint AS bindings,
       (SELECT COALESCE(sum(version), 0) + count(*) FROM backplane.rule_current)::bigint AS rules,
       (SELECT COALESCE(max(updated_at), 'epoch'::timestamptz) FROM backplane.rule)::timestamptz AS rules_updated_at;

-- name: NotifyBindings :exec
-- Tells every replica listening on the channel that bindings or rules
-- changed (Manager.Changes).
SELECT pg_notify('backplane_bindings', '');

-- name: GetRuleEvent :one
-- The event of the rule's latest definition (a delete keeps the one before it).
SELECT COALESCE(definition->>'event', '')::text AS event
FROM backplane.rule_version
WHERE rule_id = @rule_id AND definition IS NOT NULL
ORDER BY version DESC LIMIT 1;
