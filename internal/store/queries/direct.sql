-- Runs of the direct runtime (internal/direct).

-- name: ClaimDirectRun :one
-- A new run, or a failed run of the same id claimed again (a rule's event
-- redelivered); no row when the id is running or completed already.
INSERT INTO backplane.direct_run (id, kind, source, version, test, caller, status, input, program)
VALUES (@id, @kind, @source, @version, @test, @caller, 'running', @input, @program)
ON CONFLICT (id) DO UPDATE SET
  status = 'running', attempt = backplane.direct_run.attempt + 1, started_at = clock_timestamp(),
  closed_at = NULL, result = NULL, error = '', error_type = '', input = EXCLUDED.input, program = EXCLUDED.program,
  version = EXCLUDED.version, caller = EXCLUDED.caller
WHERE backplane.direct_run.status = 'failed'
RETURNING id, attempt, started_at;

-- name: FinishDirectRun :exec
UPDATE backplane.direct_run
SET status = @status, result = @result, error = @error, error_type = @error_type, closed_at = clock_timestamp()
WHERE id = @id;

-- name: GetDirectRun :one
SELECT id, kind, source, version, test, caller, status, attempt, input, result, error, error_type, program,
  started_at, closed_at
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
