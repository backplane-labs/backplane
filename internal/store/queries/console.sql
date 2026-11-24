-- name: CreateSession :one
INSERT INTO backplane.console_session (token_hash, created_at, expires_at, last_seen_at, address, user_agent)
VALUES (@token_hash, @created_at, @expires_at, @created_at, @address, @user_agent)
RETURNING id;

-- name: GetSessionByToken :one
SELECT id, created_at, expires_at, last_seen_at, address, user_agent
FROM backplane.console_session WHERE token_hash = @token_hash;

-- name: GetSession :one
SELECT id, created_at, expires_at, last_seen_at, address, user_agent
FROM backplane.console_session WHERE id = @id;

-- name: TouchSession :exec
UPDATE backplane.console_session SET last_seen_at = @last_seen_at
WHERE id = @id AND last_seen_at < @last_seen_at;

-- name: ListSessions :many
SELECT id, created_at, expires_at, last_seen_at, address, user_agent
FROM backplane.console_session ORDER BY created_at DESC;

-- name: DeleteSession :execrows
DELETE FROM backplane.console_session WHERE id = @id;

-- name: DeleteOtherSessions :execrows
DELETE FROM backplane.console_session WHERE id <> @id;

-- name: DeleteStaleSessions :execrows
DELETE FROM backplane.console_session WHERE expires_at <= @now OR last_seen_at <= @idle_since;
