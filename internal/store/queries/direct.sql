-- Runs of the direct runtime (internal/direct).

-- name: PutDirectProgram :exec
-- The program a run is about to reference; one row per content.
INSERT INTO backplane.direct_program (hash, spec) VALUES (@hash, @spec)
ON CONFLICT (hash) DO NOTHING;

-- name: ClaimDirectRun :one
-- A new run, or a run of the same id claimed again: a failed one (a rule's
-- event redelivered; a keyed hook call repeated, which also runs a
-- canceled one again) or a running one past its deadline (its replica
-- died). No row when the id is running or completed already. The run's
-- deadline is lease_ms from now.
INSERT INTO backplane.direct_run (id, kind, source, version, test, caller, status, input, program_hash, deadline_at)
VALUES (@id, @kind, @source, @version, @test, @caller, 'running', @input, @program_hash,
  clock_timestamp() + @lease_ms::bigint * interval '1 millisecond')
ON CONFLICT (id) DO UPDATE SET
  status = 'running', attempt = backplane.direct_run.attempt + 1, started_at = clock_timestamp(),
  deadline_at = EXCLUDED.deadline_at,
  closed_at = NULL, result = NULL, reply = NULL, error = '', error_type = '', incomplete = false,
  input = EXCLUDED.input, program_hash = EXCLUDED.program_hash,
  version = EXCLUDED.version, caller = EXCLUDED.caller
WHERE backplane.direct_run.status = 'failed'
  OR (@rerun_canceled::boolean AND backplane.direct_run.status = 'canceled')
  OR (backplane.direct_run.status = 'running' AND backplane.direct_run.deadline_at < clock_timestamp())
RETURNING id, attempt, started_at;

-- name: FinishDirectRun :exec
UPDATE backplane.direct_run
SET status = @status, result = @result, reply = @reply, error = @error, error_type = @error_type,
  incomplete = @incomplete, closed_at = clock_timestamp()
WHERE id = @id;

-- name: GetDirectRunReply :one
-- The unmasked reply of a keyed hook call's run, for a repeated call.
SELECT status, reply FROM backplane.direct_run WHERE id = @id;

-- name: GetDirectRunStatus :one
SELECT status FROM backplane.direct_run WHERE id = @id;

-- name: SweepDirectRuns :execrows
-- Runs still running past their deadline: their end was never recorded
-- (the record was lost, or the replica died). Closed as failed,
-- incomplete.
UPDATE backplane.direct_run
SET status = 'failed', error = @error, incomplete = true, closed_at = clock_timestamp()
WHERE status = 'running' AND deadline_at < clock_timestamp();

-- name: GetDirectRun :one
SELECT id, kind, source, version, test, caller, status, attempt, input, result, error, error_type, program_hash,
  incomplete, started_at, closed_at
FROM backplane.direct_run WHERE id = @id;

-- name: ListDirectRuns :many
SELECT id, kind, source, version, test, caller, status, attempt, error, error_type, started_at, closed_at
FROM backplane.direct_run
WHERE kind = @kind AND source = @source AND test = @test
  AND (@status::text = '' OR status = @status::text)
  AND (started_at, id) < (@before_at::timestamptz, @before_id::text)
ORDER BY started_at DESC, id DESC
LIMIT @page_size;

-- name: UpsertDirectRunStep :exec
INSERT INTO backplane.direct_run_step
  (run_id, label, seq, step, parent, item, activity, status, input, output, error, error_type, scheduled_at, closed_at)
VALUES (@run_id, @label, @seq, @step, @parent, @item, @activity, @status, @input, @output, @error, @error_type,
  @scheduled_at, @closed_at)
ON CONFLICT (run_id, label) DO UPDATE SET
  status = EXCLUDED.status, output = EXCLUDED.output, error = EXCLUDED.error, error_type = EXCLUDED.error_type,
  closed_at = EXCLUDED.closed_at;

-- name: ListDirectRunSteps :many
SELECT run_id, label, seq, step, parent, item, activity, status, input, output, error, error_type, scheduled_at,
  closed_at
FROM backplane.direct_run_step WHERE run_id = @run_id ORDER BY seq;

-- name: ExpireDirectRuns :exec
DELETE FROM backplane.direct_run
WHERE id IN (
  SELECT id FROM backplane.direct_run
  WHERE started_at < @cutoff AND status <> 'running'
  ORDER BY started_at LIMIT @batch_size
);

-- name: ExpireDirectPrograms :exec
-- Programs no run references any more.
DELETE FROM backplane.direct_program p
WHERE NOT EXISTS (SELECT 1 FROM backplane.direct_run r WHERE r.program_hash = p.hash);
