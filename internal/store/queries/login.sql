-- name: CreateLoginState :exec
INSERT INTO backplane.console_login_state (singleton) VALUES (true)
ON CONFLICT (singleton) DO NOTHING;

-- name: LoginNow :one
SELECT COALESCE(clock_timestamp(), 'epoch'::timestamptz) AS now;

-- name: GetLoginState :one
SELECT failures, forget_at, blocked_until FROM backplane.console_login_state WHERE singleton;

-- name: LockLoginState :one
SELECT failures, forget_at, blocked_until FROM backplane.console_login_state WHERE singleton FOR UPDATE;

-- name: SetLoginFailure :exec
UPDATE backplane.console_login_state
SET failures = @failures, forget_at = @forget_at, blocked_until = @blocked_until
WHERE singleton;

-- name: ResetLoginFailures :exec
UPDATE backplane.console_login_state SET failures = 0, forget_at = 'epoch' WHERE singleton;

-- name: TakeLoginAttempt :one
INSERT INTO backplane.console_login_attempt (address, attempts, expires_at)
VALUES (@address, 1, @expires_at)
ON CONFLICT (address) DO UPDATE SET
  attempts = CASE WHEN backplane.console_login_attempt.expires_at <= @now
                  THEN 1 ELSE backplane.console_login_attempt.attempts + 1 END,
  expires_at = CASE WHEN backplane.console_login_attempt.expires_at <= @now
                    THEN @expires_at ELSE backplane.console_login_attempt.expires_at END
WHERE backplane.console_login_attempt.expires_at <= @now
   OR backplane.console_login_attempt.attempts < @burst::bigint
RETURNING attempts, expires_at;

-- name: GetLoginAttempt :one
SELECT attempts, expires_at FROM backplane.console_login_attempt WHERE address = @address;

-- name: DeleteExpiredLoginAttempts :execrows
DELETE FROM backplane.console_login_attempt WHERE address IN (
  SELECT address FROM backplane.console_login_attempt WHERE expires_at <= clock_timestamp()
  ORDER BY expires_at LIMIT 1000 FOR UPDATE SKIP LOCKED
);
