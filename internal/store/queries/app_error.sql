-- Application errors (internal/apperrors).

-- name: TouchErrorGroup :one
-- Counts one occurrence in its group, creating it; the windows reset when
-- the minute or the day changed. Returns the counts of the current windows:
-- the caller stores the occurrence while they are within the burst limits.
INSERT INTO backplane.app_error_group
  (fingerprint, service, type, message, first_seen, last_seen, count, stored, minute_start, minute_count, day_start, day_count)
VALUES (@fingerprint, @service, @type, @message, @time, @time, 1, 0, @minute, 1, @day, 1)
ON CONFLICT (fingerprint, service) DO UPDATE SET
  count = backplane.app_error_group.count + 1,
  first_seen = LEAST(backplane.app_error_group.first_seen, EXCLUDED.first_seen),
  last_seen = GREATEST(backplane.app_error_group.last_seen, EXCLUDED.last_seen),
  minute_start = CASE WHEN backplane.app_error_group.minute_start < EXCLUDED.minute_start THEN EXCLUDED.minute_start
    ELSE backplane.app_error_group.minute_start END,
  minute_count = CASE WHEN backplane.app_error_group.minute_start < EXCLUDED.minute_start THEN 1
    ELSE backplane.app_error_group.minute_count + 1 END,
  day_start = CASE WHEN backplane.app_error_group.day_start < EXCLUDED.day_start THEN EXCLUDED.day_start
    ELSE backplane.app_error_group.day_start END,
  day_count = CASE WHEN backplane.app_error_group.day_start < EXCLUDED.day_start THEN 1
    ELSE backplane.app_error_group.day_count + 1 END,
  -- An occurrence after a resolution reopens the group (a regression); a
  -- late record from before it does not.
  state = CASE WHEN backplane.app_error_group.state = 'resolved' AND EXCLUDED.last_seen > backplane.app_error_group.state_at
    THEN 'open' ELSE backplane.app_error_group.state END,
  regressed_at = CASE WHEN backplane.app_error_group.state = 'resolved' AND EXCLUDED.last_seen > backplane.app_error_group.state_at
    THEN EXCLUDED.last_seen ELSE backplane.app_error_group.regressed_at END
RETURNING minute_count, day_count;

-- name: InsertError :execrows
INSERT INTO backplane.app_error (id, key, fingerprint, service, time, environment, release, instance, origin, ingest,
  event_id, type, message, stacktrace, severity, severity_text, event_name, mechanism, handled, group_key, trace_id,
  span_id, runtime_id, scope, body, attributes, resource, truncated)
VALUES (@id, @key, @fingerprint, @service, @time, @environment, @release, @instance, @origin, @ingest,
  @event_id, @type, @message, @stacktrace, @severity, @severity_text, @event_name, @mechanism, @handled, @group_key,
  @trace_id, @span_id, @runtime_id, @scope, @body, @attributes, @resource, @truncated)
ON CONFLICT (key) DO NOTHING;

-- name: CountErrorStored :exec
UPDATE backplane.app_error_group SET stored = stored + 1 WHERE fingerprint = @fingerprint AND service = @service;

-- name: UncountErrorDuplicate :exec
-- A retried record was already stored: its touch counted it twice.
UPDATE backplane.app_error_group SET count = GREATEST(count - 1, stored),
  minute_count = GREATEST(minute_count - 1, 0), day_count = GREATEST(day_count - 1, 0)
WHERE fingerprint = @fingerprint AND service = @service;

-- name: GetError :one
SELECT id, key, fingerprint, service, time, received_at, environment, release, instance, origin, ingest, event_id,
  type, message, stacktrace, severity, severity_text, event_name, mechanism, handled, group_key, trace_id, span_id,
  runtime_id, scope, body, attributes, resource, truncated
FROM backplane.app_error WHERE id = @id;

-- name: GetErrorGroup :one
SELECT fingerprint, service, type, message, first_seen, last_seen, count, stored, state, state_at, state_by, state_note, regressed_at
FROM backplane.app_error_group WHERE fingerprint = @fingerprint AND service = @service;

-- name: SetErrorGroupState :one
-- Triage: resolving or muting a group, or reopening it; a change clears
-- the regression mark.
UPDATE backplane.app_error_group
SET state = @state, state_at = @at, state_by = @by, state_note = @note, regressed_at = NULL
WHERE fingerprint = @fingerprint AND service = @service
RETURNING fingerprint, service, type, message, first_seen, last_seen, count, stored, state, state_at, state_by, state_note, regressed_at;

-- name: ExpireErrors :exec
DELETE FROM backplane.app_error WHERE id IN (
  SELECT id FROM backplane.app_error WHERE time < @cutoff ORDER BY time LIMIT @batch_size
);

-- name: ExpireErrorGroups :exec
DELETE FROM backplane.app_error_group g
WHERE g.last_seen < @cutoff
  AND NOT EXISTS (SELECT 1 FROM backplane.app_error e WHERE e.fingerprint = g.fingerprint AND e.service = g.service);

-- name: CountExpiredErrors :one
SELECT count(*)::bigint AS count FROM backplane.app_error WHERE time < @cutoff;
