-- name: CreateAuditClock :exec
INSERT INTO backplane.audit_clock (singleton) VALUES (true) ON CONFLICT DO NOTHING;

-- name: NextAuditSequence :one
UPDATE backplane.audit_clock SET sequence = sequence + 1 WHERE singleton = true
RETURNING sequence;

-- name: GetAuditClock :one
SELECT sequence, retained_after FROM backplane.audit_clock WHERE singleton = true;

-- name: InsertAuditEntry :one
INSERT INTO backplane.audit_entry (sequence, id, actor, action, subject, outcome, operation_id, detail, service)
VALUES (@sequence, @id, @actor, @action, @subject, @outcome, @operation_id, @detail, @service)
RETURNING sequence, id, created_at, actor, action, subject, outcome, operation_id, detail, service;

-- name: InsertAuditOutbox :exec
INSERT INTO backplane.audit_outbox (sequence) VALUES (@sequence);

-- name: GetAuditEntry :one
SELECT sequence, id, created_at, actor, action, subject, outcome, operation_id, detail, service
FROM backplane.audit_entry WHERE sequence = @sequence;

-- name: ClaimAuditOutbox :many
UPDATE backplane.audit_outbox SET lease = @lease,
  leased_until = now() + (@lease_seconds::bigint * interval '1 second'), attempts = attempts + 1
WHERE sequence IN (
  SELECT sequence FROM backplane.audit_outbox
  WHERE published_at IS NULL AND available_at <= now() AND leased_until <= now()
  ORDER BY sequence LIMIT @batch_size FOR UPDATE SKIP LOCKED
)
RETURNING sequence, lease;

-- name: AcknowledgeAuditOutbox :exec
UPDATE backplane.audit_outbox SET published_at = now(), lease = NULL
WHERE sequence = @sequence AND lease = @lease;

-- name: RetryAuditOutbox :exec
UPDATE backplane.audit_outbox SET lease = NULL, leased_until = '-infinity',
  available_at = now() + (@retry_seconds::bigint * interval '1 second')
WHERE sequence = @sequence AND lease = @lease AND published_at IS NULL;

-- name: GetAuditExpiryBoundary :one
SELECT COALESCE(min(e.sequence) - 1,
  (SELECT sequence FROM backplane.audit_clock WHERE singleton = true))::bigint AS through_sequence
FROM backplane.audit_entry e
LEFT JOIN backplane.audit_outbox o ON o.sequence = e.sequence
WHERE e.created_at >= @cutoff OR (o.sequence IS NOT NULL AND o.published_at IS NULL);

-- name: DeleteExpiredAuditOutbox :exec
DELETE FROM backplane.audit_outbox WHERE sequence <= @through_sequence AND published_at IS NOT NULL;

-- name: DeleteExpiredAuditEntries :exec
DELETE FROM backplane.audit_entry WHERE sequence <= @through_sequence;

-- name: AdvanceAuditRetention :exec
UPDATE backplane.audit_clock SET retained_after = @through_sequence
WHERE singleton = true AND retained_after < @through_sequence AND sequence >= @through_sequence;

-- name: LockAuditClock :exec
LOCK TABLE backplane.audit_clock IN EXCLUSIVE MODE;
