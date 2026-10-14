-- Reading application errors (internal/apperrors). Every listing takes the
-- same filter: the range and, per field, the values to keep (_in), to drop
-- (_not), to match (_like, _not_like, _prefix) and whether it must be set
-- (_exists: 1 set, -1 unset, 0 either); an empty array leaves the field
-- alone.

-- name: SearchErrors :many
SELECT e.id, e.fingerprint, e.service, e.time, e.received_at, e.environment, e.release, e.instance, e.origin, e.ingest,
  e.event_id, e.type, e.message, e.severity, e.mechanism, e.handled, e.group_key, e.trace_id, e.span_id, e.runtime_id,
  e.truncated
FROM backplane.app_error e
WHERE e.time >= @start AND e.time < @end
  AND (cardinality(@service_in::text[]) = 0 OR e.service = ANY(@service_in::text[]))
  AND (cardinality(@service_not::text[]) = 0 OR NOT (e.service = ANY(@service_not::text[])))
  AND (cardinality(@service_prefix::text[]) = 0 OR EXISTS (SELECT 1 FROM unnest(@service_prefix::text[]) p WHERE e.service LIKE p || '%'))
  AND (cardinality(@environment_in::text[]) = 0 OR e.environment = ANY(@environment_in::text[]))
  AND (cardinality(@environment_not::text[]) = 0 OR NOT (e.environment = ANY(@environment_not::text[])))
  AND (@environment_exists::int = 0 OR (@environment_exists::int > 0) = (e.environment <> ''))
  AND (cardinality(@release_in::text[]) = 0 OR e.release = ANY(@release_in::text[]))
  AND (cardinality(@release_not::text[]) = 0 OR NOT (e.release = ANY(@release_not::text[])))
  AND (cardinality(@release_prefix::text[]) = 0 OR EXISTS (SELECT 1 FROM unnest(@release_prefix::text[]) p WHERE e.release LIKE p || '%'))
  AND (@release_exists::int = 0 OR (@release_exists::int > 0) = (e.release <> ''))
  AND (cardinality(@type_in::text[]) = 0 OR e.type = ANY(@type_in::text[]))
  AND (cardinality(@type_not::text[]) = 0 OR NOT (e.type = ANY(@type_not::text[])))
  AND (cardinality(@type_like::text[]) = 0 OR EXISTS (SELECT 1 FROM unnest(@type_like::text[]) p WHERE e.type ILIKE '%' || p || '%'))
  AND (cardinality(@type_not_like::text[]) = 0 OR NOT EXISTS (SELECT 1 FROM unnest(@type_not_like::text[]) p WHERE e.type ILIKE '%' || p || '%'))
  AND (cardinality(@type_prefix::text[]) = 0 OR EXISTS (SELECT 1 FROM unnest(@type_prefix::text[]) p WHERE e.type LIKE p || '%'))
  AND (cardinality(@message_in::text[]) = 0 OR e.message = ANY(@message_in::text[]))
  AND (cardinality(@message_not::text[]) = 0 OR NOT (e.message = ANY(@message_not::text[])))
  AND (cardinality(@message_like::text[]) = 0 OR EXISTS (SELECT 1 FROM unnest(@message_like::text[]) p WHERE e.message ILIKE '%' || p || '%'))
  AND (cardinality(@message_not_like::text[]) = 0 OR NOT EXISTS (SELECT 1 FROM unnest(@message_not_like::text[]) p WHERE e.message ILIKE '%' || p || '%'))
  AND (cardinality(@message_prefix::text[]) = 0 OR EXISTS (SELECT 1 FROM unnest(@message_prefix::text[]) p WHERE e.message LIKE p || '%'))
  AND (cardinality(@trace_in::text[]) = 0 OR e.trace_id = ANY(@trace_in::text[]))
  AND (cardinality(@trace_not::text[]) = 0 OR NOT (e.trace_id = ANY(@trace_not::text[])))
  AND (@trace_exists::int = 0 OR (@trace_exists::int > 0) = (e.trace_id <> ''))
  AND (cardinality(@runtime_in::text[]) = 0 OR e.runtime_id = ANY(@runtime_in::text[]))
  AND (cardinality(@runtime_not::text[]) = 0 OR NOT (e.runtime_id = ANY(@runtime_not::text[])))
  AND (@runtime_exists::int = 0 OR (@runtime_exists::int > 0) = (e.runtime_id <> ''))
  AND (cardinality(@group_key_in::text[]) = 0 OR e.group_key = ANY(@group_key_in::text[]))
  AND (cardinality(@group_key_not::text[]) = 0 OR NOT (e.group_key = ANY(@group_key_not::text[])))
  AND (@group_key_exists::int = 0 OR (@group_key_exists::int > 0) = (e.group_key <> ''))
  AND (cardinality(@origin_in::text[]) = 0 OR e.origin = ANY(@origin_in::text[]))
  AND (cardinality(@origin_not::text[]) = 0 OR NOT (e.origin = ANY(@origin_not::text[])))
  AND (cardinality(@fingerprint_in::bytea[]) = 0 OR e.fingerprint = ANY(@fingerprint_in::bytea[]))
  AND (cardinality(@fingerprint_not::bytea[]) = 0 OR NOT (e.fingerprint = ANY(@fingerprint_not::bytea[])))
  AND (@handled::int = 0 OR (@handled::int > 0 AND e.handled IS TRUE) OR (@handled::int < 0 AND e.handled IS FALSE))
  AND (cardinality(@ingest_in::text[]) = 0 OR e.ingest = ANY(@ingest_in::text[]))
  AND (cardinality(@ingest_not::text[]) = 0 OR NOT (e.ingest = ANY(@ingest_not::text[])))
  AND (cardinality(@instance_in::text[]) = 0 OR e.instance = ANY(@instance_in::text[]))
  AND (cardinality(@instance_not::text[]) = 0 OR NOT (e.instance = ANY(@instance_not::text[])))
  AND (@text::text = '' OR e.message ILIKE '%' || @text::text || '%' OR e.type ILIKE '%' || @text::text || '%'
    OR (@text_in_stack::bool AND e.stacktrace ILIKE '%' || @text::text || '%'))
  AND (e.time, e.id) < (@before_time::timestamptz, @before_id::uuid)
ORDER BY e.time DESC, e.id DESC
LIMIT @page_size;

-- name: ErrorHistogram :many
SELECT date_bin(@step::interval, e.time, @start::timestamptz)::timestamptz AS bucket, count(*)::bigint AS count
FROM backplane.app_error e
WHERE e.time >= @start AND e.time < @end
  AND (cardinality(@service_in::text[]) = 0 OR e.service = ANY(@service_in::text[]))
  AND (cardinality(@service_not::text[]) = 0 OR NOT (e.service = ANY(@service_not::text[])))
  AND (cardinality(@service_prefix::text[]) = 0 OR EXISTS (SELECT 1 FROM unnest(@service_prefix::text[]) p WHERE e.service LIKE p || '%'))
  AND (cardinality(@environment_in::text[]) = 0 OR e.environment = ANY(@environment_in::text[]))
  AND (cardinality(@environment_not::text[]) = 0 OR NOT (e.environment = ANY(@environment_not::text[])))
  AND (@environment_exists::int = 0 OR (@environment_exists::int > 0) = (e.environment <> ''))
  AND (cardinality(@release_in::text[]) = 0 OR e.release = ANY(@release_in::text[]))
  AND (cardinality(@release_not::text[]) = 0 OR NOT (e.release = ANY(@release_not::text[])))
  AND (cardinality(@release_prefix::text[]) = 0 OR EXISTS (SELECT 1 FROM unnest(@release_prefix::text[]) p WHERE e.release LIKE p || '%'))
  AND (@release_exists::int = 0 OR (@release_exists::int > 0) = (e.release <> ''))
  AND (cardinality(@type_in::text[]) = 0 OR e.type = ANY(@type_in::text[]))
  AND (cardinality(@type_not::text[]) = 0 OR NOT (e.type = ANY(@type_not::text[])))
  AND (cardinality(@type_like::text[]) = 0 OR EXISTS (SELECT 1 FROM unnest(@type_like::text[]) p WHERE e.type ILIKE '%' || p || '%'))
  AND (cardinality(@type_not_like::text[]) = 0 OR NOT EXISTS (SELECT 1 FROM unnest(@type_not_like::text[]) p WHERE e.type ILIKE '%' || p || '%'))
  AND (cardinality(@type_prefix::text[]) = 0 OR EXISTS (SELECT 1 FROM unnest(@type_prefix::text[]) p WHERE e.type LIKE p || '%'))
  AND (cardinality(@message_in::text[]) = 0 OR e.message = ANY(@message_in::text[]))
  AND (cardinality(@message_not::text[]) = 0 OR NOT (e.message = ANY(@message_not::text[])))
  AND (cardinality(@message_like::text[]) = 0 OR EXISTS (SELECT 1 FROM unnest(@message_like::text[]) p WHERE e.message ILIKE '%' || p || '%'))
  AND (cardinality(@message_not_like::text[]) = 0 OR NOT EXISTS (SELECT 1 FROM unnest(@message_not_like::text[]) p WHERE e.message ILIKE '%' || p || '%'))
  AND (cardinality(@message_prefix::text[]) = 0 OR EXISTS (SELECT 1 FROM unnest(@message_prefix::text[]) p WHERE e.message LIKE p || '%'))
  AND (cardinality(@trace_in::text[]) = 0 OR e.trace_id = ANY(@trace_in::text[]))
  AND (cardinality(@trace_not::text[]) = 0 OR NOT (e.trace_id = ANY(@trace_not::text[])))
  AND (@trace_exists::int = 0 OR (@trace_exists::int > 0) = (e.trace_id <> ''))
  AND (cardinality(@runtime_in::text[]) = 0 OR e.runtime_id = ANY(@runtime_in::text[]))
  AND (cardinality(@runtime_not::text[]) = 0 OR NOT (e.runtime_id = ANY(@runtime_not::text[])))
  AND (@runtime_exists::int = 0 OR (@runtime_exists::int > 0) = (e.runtime_id <> ''))
  AND (cardinality(@group_key_in::text[]) = 0 OR e.group_key = ANY(@group_key_in::text[]))
  AND (cardinality(@group_key_not::text[]) = 0 OR NOT (e.group_key = ANY(@group_key_not::text[])))
  AND (@group_key_exists::int = 0 OR (@group_key_exists::int > 0) = (e.group_key <> ''))
  AND (cardinality(@origin_in::text[]) = 0 OR e.origin = ANY(@origin_in::text[]))
  AND (cardinality(@origin_not::text[]) = 0 OR NOT (e.origin = ANY(@origin_not::text[])))
  AND (cardinality(@fingerprint_in::bytea[]) = 0 OR e.fingerprint = ANY(@fingerprint_in::bytea[]))
  AND (cardinality(@fingerprint_not::bytea[]) = 0 OR NOT (e.fingerprint = ANY(@fingerprint_not::bytea[])))
  AND (@handled::int = 0 OR (@handled::int > 0 AND e.handled IS TRUE) OR (@handled::int < 0 AND e.handled IS FALSE))
  AND (cardinality(@ingest_in::text[]) = 0 OR e.ingest = ANY(@ingest_in::text[]))
  AND (cardinality(@ingest_not::text[]) = 0 OR NOT (e.ingest = ANY(@ingest_not::text[])))
  AND (cardinality(@instance_in::text[]) = 0 OR e.instance = ANY(@instance_in::text[]))
  AND (cardinality(@instance_not::text[]) = 0 OR NOT (e.instance = ANY(@instance_not::text[])))
  AND (@text::text = '' OR e.message ILIKE '%' || @text::text || '%' OR e.type ILIKE '%' || @text::text || '%'
    OR (@text_in_stack::bool AND e.stacktrace ILIKE '%' || @text::text || '%'))
GROUP BY bucket
ORDER BY bucket;

-- name: ErrorFacetService :many
SELECT e.service::text AS value, count(*)::bigint AS count, max(e.time) AS last_seen
FROM backplane.app_error e
WHERE e.time >= @start AND e.time < @end
  AND (cardinality(@service_in::text[]) = 0 OR e.service = ANY(@service_in::text[]))
  AND (cardinality(@service_not::text[]) = 0 OR NOT (e.service = ANY(@service_not::text[])))
  AND (cardinality(@service_prefix::text[]) = 0 OR EXISTS (SELECT 1 FROM unnest(@service_prefix::text[]) p WHERE e.service LIKE p || '%'))
  AND (cardinality(@environment_in::text[]) = 0 OR e.environment = ANY(@environment_in::text[]))
  AND (cardinality(@environment_not::text[]) = 0 OR NOT (e.environment = ANY(@environment_not::text[])))
  AND (@environment_exists::int = 0 OR (@environment_exists::int > 0) = (e.environment <> ''))
  AND (cardinality(@release_in::text[]) = 0 OR e.release = ANY(@release_in::text[]))
  AND (cardinality(@release_not::text[]) = 0 OR NOT (e.release = ANY(@release_not::text[])))
  AND (cardinality(@release_prefix::text[]) = 0 OR EXISTS (SELECT 1 FROM unnest(@release_prefix::text[]) p WHERE e.release LIKE p || '%'))
  AND (@release_exists::int = 0 OR (@release_exists::int > 0) = (e.release <> ''))
  AND (cardinality(@type_in::text[]) = 0 OR e.type = ANY(@type_in::text[]))
  AND (cardinality(@type_not::text[]) = 0 OR NOT (e.type = ANY(@type_not::text[])))
  AND (cardinality(@type_like::text[]) = 0 OR EXISTS (SELECT 1 FROM unnest(@type_like::text[]) p WHERE e.type ILIKE '%' || p || '%'))
  AND (cardinality(@type_not_like::text[]) = 0 OR NOT EXISTS (SELECT 1 FROM unnest(@type_not_like::text[]) p WHERE e.type ILIKE '%' || p || '%'))
  AND (cardinality(@type_prefix::text[]) = 0 OR EXISTS (SELECT 1 FROM unnest(@type_prefix::text[]) p WHERE e.type LIKE p || '%'))
  AND (cardinality(@message_in::text[]) = 0 OR e.message = ANY(@message_in::text[]))
  AND (cardinality(@message_not::text[]) = 0 OR NOT (e.message = ANY(@message_not::text[])))
  AND (cardinality(@message_like::text[]) = 0 OR EXISTS (SELECT 1 FROM unnest(@message_like::text[]) p WHERE e.message ILIKE '%' || p || '%'))
  AND (cardinality(@message_not_like::text[]) = 0 OR NOT EXISTS (SELECT 1 FROM unnest(@message_not_like::text[]) p WHERE e.message ILIKE '%' || p || '%'))
  AND (cardinality(@message_prefix::text[]) = 0 OR EXISTS (SELECT 1 FROM unnest(@message_prefix::text[]) p WHERE e.message LIKE p || '%'))
  AND (cardinality(@trace_in::text[]) = 0 OR e.trace_id = ANY(@trace_in::text[]))
  AND (cardinality(@trace_not::text[]) = 0 OR NOT (e.trace_id = ANY(@trace_not::text[])))
  AND (@trace_exists::int = 0 OR (@trace_exists::int > 0) = (e.trace_id <> ''))
  AND (cardinality(@runtime_in::text[]) = 0 OR e.runtime_id = ANY(@runtime_in::text[]))
  AND (cardinality(@runtime_not::text[]) = 0 OR NOT (e.runtime_id = ANY(@runtime_not::text[])))
  AND (@runtime_exists::int = 0 OR (@runtime_exists::int > 0) = (e.runtime_id <> ''))
  AND (cardinality(@group_key_in::text[]) = 0 OR e.group_key = ANY(@group_key_in::text[]))
  AND (cardinality(@group_key_not::text[]) = 0 OR NOT (e.group_key = ANY(@group_key_not::text[])))
  AND (@group_key_exists::int = 0 OR (@group_key_exists::int > 0) = (e.group_key <> ''))
  AND (cardinality(@origin_in::text[]) = 0 OR e.origin = ANY(@origin_in::text[]))
  AND (cardinality(@origin_not::text[]) = 0 OR NOT (e.origin = ANY(@origin_not::text[])))
  AND (cardinality(@fingerprint_in::bytea[]) = 0 OR e.fingerprint = ANY(@fingerprint_in::bytea[]))
  AND (cardinality(@fingerprint_not::bytea[]) = 0 OR NOT (e.fingerprint = ANY(@fingerprint_not::bytea[])))
  AND (@handled::int = 0 OR (@handled::int > 0 AND e.handled IS TRUE) OR (@handled::int < 0 AND e.handled IS FALSE))
  AND (cardinality(@ingest_in::text[]) = 0 OR e.ingest = ANY(@ingest_in::text[]))
  AND (cardinality(@ingest_not::text[]) = 0 OR NOT (e.ingest = ANY(@ingest_not::text[])))
  AND (cardinality(@instance_in::text[]) = 0 OR e.instance = ANY(@instance_in::text[]))
  AND (cardinality(@instance_not::text[]) = 0 OR NOT (e.instance = ANY(@instance_not::text[])))
  AND (@text::text = '' OR e.message ILIKE '%' || @text::text || '%' OR e.type ILIKE '%' || @text::text || '%'
    OR (@text_in_stack::bool AND e.stacktrace ILIKE '%' || @text::text || '%'))
GROUP BY e.service
ORDER BY count DESC, value
LIMIT @page_size;

-- name: ErrorFacetType :many
SELECT e.type::text AS value, count(*)::bigint AS count, max(e.time) AS last_seen
FROM backplane.app_error e
WHERE e.time >= @start AND e.time < @end
  AND (cardinality(@service_in::text[]) = 0 OR e.service = ANY(@service_in::text[]))
  AND (cardinality(@service_not::text[]) = 0 OR NOT (e.service = ANY(@service_not::text[])))
  AND (cardinality(@service_prefix::text[]) = 0 OR EXISTS (SELECT 1 FROM unnest(@service_prefix::text[]) p WHERE e.service LIKE p || '%'))
  AND (cardinality(@environment_in::text[]) = 0 OR e.environment = ANY(@environment_in::text[]))
  AND (cardinality(@environment_not::text[]) = 0 OR NOT (e.environment = ANY(@environment_not::text[])))
  AND (@environment_exists::int = 0 OR (@environment_exists::int > 0) = (e.environment <> ''))
  AND (cardinality(@release_in::text[]) = 0 OR e.release = ANY(@release_in::text[]))
  AND (cardinality(@release_not::text[]) = 0 OR NOT (e.release = ANY(@release_not::text[])))
  AND (cardinality(@release_prefix::text[]) = 0 OR EXISTS (SELECT 1 FROM unnest(@release_prefix::text[]) p WHERE e.release LIKE p || '%'))
  AND (@release_exists::int = 0 OR (@release_exists::int > 0) = (e.release <> ''))
  AND (cardinality(@type_in::text[]) = 0 OR e.type = ANY(@type_in::text[]))
  AND (cardinality(@type_not::text[]) = 0 OR NOT (e.type = ANY(@type_not::text[])))
  AND (cardinality(@type_like::text[]) = 0 OR EXISTS (SELECT 1 FROM unnest(@type_like::text[]) p WHERE e.type ILIKE '%' || p || '%'))
  AND (cardinality(@type_not_like::text[]) = 0 OR NOT EXISTS (SELECT 1 FROM unnest(@type_not_like::text[]) p WHERE e.type ILIKE '%' || p || '%'))
  AND (cardinality(@type_prefix::text[]) = 0 OR EXISTS (SELECT 1 FROM unnest(@type_prefix::text[]) p WHERE e.type LIKE p || '%'))
  AND (cardinality(@message_in::text[]) = 0 OR e.message = ANY(@message_in::text[]))
  AND (cardinality(@message_not::text[]) = 0 OR NOT (e.message = ANY(@message_not::text[])))
  AND (cardinality(@message_like::text[]) = 0 OR EXISTS (SELECT 1 FROM unnest(@message_like::text[]) p WHERE e.message ILIKE '%' || p || '%'))
  AND (cardinality(@message_not_like::text[]) = 0 OR NOT EXISTS (SELECT 1 FROM unnest(@message_not_like::text[]) p WHERE e.message ILIKE '%' || p || '%'))
  AND (cardinality(@message_prefix::text[]) = 0 OR EXISTS (SELECT 1 FROM unnest(@message_prefix::text[]) p WHERE e.message LIKE p || '%'))
  AND (cardinality(@trace_in::text[]) = 0 OR e.trace_id = ANY(@trace_in::text[]))
  AND (cardinality(@trace_not::text[]) = 0 OR NOT (e.trace_id = ANY(@trace_not::text[])))
  AND (@trace_exists::int = 0 OR (@trace_exists::int > 0) = (e.trace_id <> ''))
  AND (cardinality(@runtime_in::text[]) = 0 OR e.runtime_id = ANY(@runtime_in::text[]))
  AND (cardinality(@runtime_not::text[]) = 0 OR NOT (e.runtime_id = ANY(@runtime_not::text[])))
  AND (@runtime_exists::int = 0 OR (@runtime_exists::int > 0) = (e.runtime_id <> ''))
  AND (cardinality(@group_key_in::text[]) = 0 OR e.group_key = ANY(@group_key_in::text[]))
  AND (cardinality(@group_key_not::text[]) = 0 OR NOT (e.group_key = ANY(@group_key_not::text[])))
  AND (@group_key_exists::int = 0 OR (@group_key_exists::int > 0) = (e.group_key <> ''))
  AND (cardinality(@origin_in::text[]) = 0 OR e.origin = ANY(@origin_in::text[]))
  AND (cardinality(@origin_not::text[]) = 0 OR NOT (e.origin = ANY(@origin_not::text[])))
  AND (cardinality(@fingerprint_in::bytea[]) = 0 OR e.fingerprint = ANY(@fingerprint_in::bytea[]))
  AND (cardinality(@fingerprint_not::bytea[]) = 0 OR NOT (e.fingerprint = ANY(@fingerprint_not::bytea[])))
  AND (@handled::int = 0 OR (@handled::int > 0 AND e.handled IS TRUE) OR (@handled::int < 0 AND e.handled IS FALSE))
  AND (cardinality(@ingest_in::text[]) = 0 OR e.ingest = ANY(@ingest_in::text[]))
  AND (cardinality(@ingest_not::text[]) = 0 OR NOT (e.ingest = ANY(@ingest_not::text[])))
  AND (cardinality(@instance_in::text[]) = 0 OR e.instance = ANY(@instance_in::text[]))
  AND (cardinality(@instance_not::text[]) = 0 OR NOT (e.instance = ANY(@instance_not::text[])))
  AND (@text::text = '' OR e.message ILIKE '%' || @text::text || '%' OR e.type ILIKE '%' || @text::text || '%'
    OR (@text_in_stack::bool AND e.stacktrace ILIKE '%' || @text::text || '%'))
GROUP BY e.type
ORDER BY count DESC, value
LIMIT @page_size;

-- name: ErrorFacetEnvironment :many
SELECT e.environment::text AS value, count(*)::bigint AS count, max(e.time) AS last_seen
FROM backplane.app_error e
WHERE e.time >= @start AND e.time < @end
  AND (cardinality(@service_in::text[]) = 0 OR e.service = ANY(@service_in::text[]))
  AND (cardinality(@service_not::text[]) = 0 OR NOT (e.service = ANY(@service_not::text[])))
  AND (cardinality(@service_prefix::text[]) = 0 OR EXISTS (SELECT 1 FROM unnest(@service_prefix::text[]) p WHERE e.service LIKE p || '%'))
  AND (cardinality(@environment_in::text[]) = 0 OR e.environment = ANY(@environment_in::text[]))
  AND (cardinality(@environment_not::text[]) = 0 OR NOT (e.environment = ANY(@environment_not::text[])))
  AND (@environment_exists::int = 0 OR (@environment_exists::int > 0) = (e.environment <> ''))
  AND (cardinality(@release_in::text[]) = 0 OR e.release = ANY(@release_in::text[]))
  AND (cardinality(@release_not::text[]) = 0 OR NOT (e.release = ANY(@release_not::text[])))
  AND (cardinality(@release_prefix::text[]) = 0 OR EXISTS (SELECT 1 FROM unnest(@release_prefix::text[]) p WHERE e.release LIKE p || '%'))
  AND (@release_exists::int = 0 OR (@release_exists::int > 0) = (e.release <> ''))
  AND (cardinality(@type_in::text[]) = 0 OR e.type = ANY(@type_in::text[]))
  AND (cardinality(@type_not::text[]) = 0 OR NOT (e.type = ANY(@type_not::text[])))
  AND (cardinality(@type_like::text[]) = 0 OR EXISTS (SELECT 1 FROM unnest(@type_like::text[]) p WHERE e.type ILIKE '%' || p || '%'))
  AND (cardinality(@type_not_like::text[]) = 0 OR NOT EXISTS (SELECT 1 FROM unnest(@type_not_like::text[]) p WHERE e.type ILIKE '%' || p || '%'))
  AND (cardinality(@type_prefix::text[]) = 0 OR EXISTS (SELECT 1 FROM unnest(@type_prefix::text[]) p WHERE e.type LIKE p || '%'))
  AND (cardinality(@message_in::text[]) = 0 OR e.message = ANY(@message_in::text[]))
  AND (cardinality(@message_not::text[]) = 0 OR NOT (e.message = ANY(@message_not::text[])))
  AND (cardinality(@message_like::text[]) = 0 OR EXISTS (SELECT 1 FROM unnest(@message_like::text[]) p WHERE e.message ILIKE '%' || p || '%'))
  AND (cardinality(@message_not_like::text[]) = 0 OR NOT EXISTS (SELECT 1 FROM unnest(@message_not_like::text[]) p WHERE e.message ILIKE '%' || p || '%'))
  AND (cardinality(@message_prefix::text[]) = 0 OR EXISTS (SELECT 1 FROM unnest(@message_prefix::text[]) p WHERE e.message LIKE p || '%'))
  AND (cardinality(@trace_in::text[]) = 0 OR e.trace_id = ANY(@trace_in::text[]))
  AND (cardinality(@trace_not::text[]) = 0 OR NOT (e.trace_id = ANY(@trace_not::text[])))
  AND (@trace_exists::int = 0 OR (@trace_exists::int > 0) = (e.trace_id <> ''))
  AND (cardinality(@runtime_in::text[]) = 0 OR e.runtime_id = ANY(@runtime_in::text[]))
  AND (cardinality(@runtime_not::text[]) = 0 OR NOT (e.runtime_id = ANY(@runtime_not::text[])))
  AND (@runtime_exists::int = 0 OR (@runtime_exists::int > 0) = (e.runtime_id <> ''))
  AND (cardinality(@group_key_in::text[]) = 0 OR e.group_key = ANY(@group_key_in::text[]))
  AND (cardinality(@group_key_not::text[]) = 0 OR NOT (e.group_key = ANY(@group_key_not::text[])))
  AND (@group_key_exists::int = 0 OR (@group_key_exists::int > 0) = (e.group_key <> ''))
  AND (cardinality(@origin_in::text[]) = 0 OR e.origin = ANY(@origin_in::text[]))
  AND (cardinality(@origin_not::text[]) = 0 OR NOT (e.origin = ANY(@origin_not::text[])))
  AND (cardinality(@fingerprint_in::bytea[]) = 0 OR e.fingerprint = ANY(@fingerprint_in::bytea[]))
  AND (cardinality(@fingerprint_not::bytea[]) = 0 OR NOT (e.fingerprint = ANY(@fingerprint_not::bytea[])))
  AND (@handled::int = 0 OR (@handled::int > 0 AND e.handled IS TRUE) OR (@handled::int < 0 AND e.handled IS FALSE))
  AND (cardinality(@ingest_in::text[]) = 0 OR e.ingest = ANY(@ingest_in::text[]))
  AND (cardinality(@ingest_not::text[]) = 0 OR NOT (e.ingest = ANY(@ingest_not::text[])))
  AND (cardinality(@instance_in::text[]) = 0 OR e.instance = ANY(@instance_in::text[]))
  AND (cardinality(@instance_not::text[]) = 0 OR NOT (e.instance = ANY(@instance_not::text[])))
  AND (@text::text = '' OR e.message ILIKE '%' || @text::text || '%' OR e.type ILIKE '%' || @text::text || '%'
    OR (@text_in_stack::bool AND e.stacktrace ILIKE '%' || @text::text || '%'))
GROUP BY e.environment
ORDER BY count DESC, value
LIMIT @page_size;

-- name: ErrorFacetRelease :many
SELECT e.release::text AS value, count(*)::bigint AS count, max(e.time) AS last_seen
FROM backplane.app_error e
WHERE e.time >= @start AND e.time < @end
  AND (cardinality(@service_in::text[]) = 0 OR e.service = ANY(@service_in::text[]))
  AND (cardinality(@service_not::text[]) = 0 OR NOT (e.service = ANY(@service_not::text[])))
  AND (cardinality(@service_prefix::text[]) = 0 OR EXISTS (SELECT 1 FROM unnest(@service_prefix::text[]) p WHERE e.service LIKE p || '%'))
  AND (cardinality(@environment_in::text[]) = 0 OR e.environment = ANY(@environment_in::text[]))
  AND (cardinality(@environment_not::text[]) = 0 OR NOT (e.environment = ANY(@environment_not::text[])))
  AND (@environment_exists::int = 0 OR (@environment_exists::int > 0) = (e.environment <> ''))
  AND (cardinality(@release_in::text[]) = 0 OR e.release = ANY(@release_in::text[]))
  AND (cardinality(@release_not::text[]) = 0 OR NOT (e.release = ANY(@release_not::text[])))
  AND (cardinality(@release_prefix::text[]) = 0 OR EXISTS (SELECT 1 FROM unnest(@release_prefix::text[]) p WHERE e.release LIKE p || '%'))
  AND (@release_exists::int = 0 OR (@release_exists::int > 0) = (e.release <> ''))
  AND (cardinality(@type_in::text[]) = 0 OR e.type = ANY(@type_in::text[]))
  AND (cardinality(@type_not::text[]) = 0 OR NOT (e.type = ANY(@type_not::text[])))
  AND (cardinality(@type_like::text[]) = 0 OR EXISTS (SELECT 1 FROM unnest(@type_like::text[]) p WHERE e.type ILIKE '%' || p || '%'))
  AND (cardinality(@type_not_like::text[]) = 0 OR NOT EXISTS (SELECT 1 FROM unnest(@type_not_like::text[]) p WHERE e.type ILIKE '%' || p || '%'))
  AND (cardinality(@type_prefix::text[]) = 0 OR EXISTS (SELECT 1 FROM unnest(@type_prefix::text[]) p WHERE e.type LIKE p || '%'))
  AND (cardinality(@message_in::text[]) = 0 OR e.message = ANY(@message_in::text[]))
  AND (cardinality(@message_not::text[]) = 0 OR NOT (e.message = ANY(@message_not::text[])))
  AND (cardinality(@message_like::text[]) = 0 OR EXISTS (SELECT 1 FROM unnest(@message_like::text[]) p WHERE e.message ILIKE '%' || p || '%'))
  AND (cardinality(@message_not_like::text[]) = 0 OR NOT EXISTS (SELECT 1 FROM unnest(@message_not_like::text[]) p WHERE e.message ILIKE '%' || p || '%'))
  AND (cardinality(@message_prefix::text[]) = 0 OR EXISTS (SELECT 1 FROM unnest(@message_prefix::text[]) p WHERE e.message LIKE p || '%'))
  AND (cardinality(@trace_in::text[]) = 0 OR e.trace_id = ANY(@trace_in::text[]))
  AND (cardinality(@trace_not::text[]) = 0 OR NOT (e.trace_id = ANY(@trace_not::text[])))
  AND (@trace_exists::int = 0 OR (@trace_exists::int > 0) = (e.trace_id <> ''))
  AND (cardinality(@runtime_in::text[]) = 0 OR e.runtime_id = ANY(@runtime_in::text[]))
  AND (cardinality(@runtime_not::text[]) = 0 OR NOT (e.runtime_id = ANY(@runtime_not::text[])))
  AND (@runtime_exists::int = 0 OR (@runtime_exists::int > 0) = (e.runtime_id <> ''))
  AND (cardinality(@group_key_in::text[]) = 0 OR e.group_key = ANY(@group_key_in::text[]))
  AND (cardinality(@group_key_not::text[]) = 0 OR NOT (e.group_key = ANY(@group_key_not::text[])))
  AND (@group_key_exists::int = 0 OR (@group_key_exists::int > 0) = (e.group_key <> ''))
  AND (cardinality(@origin_in::text[]) = 0 OR e.origin = ANY(@origin_in::text[]))
  AND (cardinality(@origin_not::text[]) = 0 OR NOT (e.origin = ANY(@origin_not::text[])))
  AND (cardinality(@fingerprint_in::bytea[]) = 0 OR e.fingerprint = ANY(@fingerprint_in::bytea[]))
  AND (cardinality(@fingerprint_not::bytea[]) = 0 OR NOT (e.fingerprint = ANY(@fingerprint_not::bytea[])))
  AND (@handled::int = 0 OR (@handled::int > 0 AND e.handled IS TRUE) OR (@handled::int < 0 AND e.handled IS FALSE))
  AND (cardinality(@ingest_in::text[]) = 0 OR e.ingest = ANY(@ingest_in::text[]))
  AND (cardinality(@ingest_not::text[]) = 0 OR NOT (e.ingest = ANY(@ingest_not::text[])))
  AND (cardinality(@instance_in::text[]) = 0 OR e.instance = ANY(@instance_in::text[]))
  AND (cardinality(@instance_not::text[]) = 0 OR NOT (e.instance = ANY(@instance_not::text[])))
  AND (@text::text = '' OR e.message ILIKE '%' || @text::text || '%' OR e.type ILIKE '%' || @text::text || '%'
    OR (@text_in_stack::bool AND e.stacktrace ILIKE '%' || @text::text || '%'))
GROUP BY e.release
ORDER BY count DESC, value
LIMIT @page_size;

-- name: ErrorFacetOrigin :many
SELECT e.origin::text AS value, count(*)::bigint AS count, max(e.time) AS last_seen
FROM backplane.app_error e
WHERE e.time >= @start AND e.time < @end
  AND (cardinality(@service_in::text[]) = 0 OR e.service = ANY(@service_in::text[]))
  AND (cardinality(@service_not::text[]) = 0 OR NOT (e.service = ANY(@service_not::text[])))
  AND (cardinality(@service_prefix::text[]) = 0 OR EXISTS (SELECT 1 FROM unnest(@service_prefix::text[]) p WHERE e.service LIKE p || '%'))
  AND (cardinality(@environment_in::text[]) = 0 OR e.environment = ANY(@environment_in::text[]))
  AND (cardinality(@environment_not::text[]) = 0 OR NOT (e.environment = ANY(@environment_not::text[])))
  AND (@environment_exists::int = 0 OR (@environment_exists::int > 0) = (e.environment <> ''))
  AND (cardinality(@release_in::text[]) = 0 OR e.release = ANY(@release_in::text[]))
  AND (cardinality(@release_not::text[]) = 0 OR NOT (e.release = ANY(@release_not::text[])))
  AND (cardinality(@release_prefix::text[]) = 0 OR EXISTS (SELECT 1 FROM unnest(@release_prefix::text[]) p WHERE e.release LIKE p || '%'))
  AND (@release_exists::int = 0 OR (@release_exists::int > 0) = (e.release <> ''))
  AND (cardinality(@type_in::text[]) = 0 OR e.type = ANY(@type_in::text[]))
  AND (cardinality(@type_not::text[]) = 0 OR NOT (e.type = ANY(@type_not::text[])))
  AND (cardinality(@type_like::text[]) = 0 OR EXISTS (SELECT 1 FROM unnest(@type_like::text[]) p WHERE e.type ILIKE '%' || p || '%'))
  AND (cardinality(@type_not_like::text[]) = 0 OR NOT EXISTS (SELECT 1 FROM unnest(@type_not_like::text[]) p WHERE e.type ILIKE '%' || p || '%'))
  AND (cardinality(@type_prefix::text[]) = 0 OR EXISTS (SELECT 1 FROM unnest(@type_prefix::text[]) p WHERE e.type LIKE p || '%'))
  AND (cardinality(@message_in::text[]) = 0 OR e.message = ANY(@message_in::text[]))
  AND (cardinality(@message_not::text[]) = 0 OR NOT (e.message = ANY(@message_not::text[])))
  AND (cardinality(@message_like::text[]) = 0 OR EXISTS (SELECT 1 FROM unnest(@message_like::text[]) p WHERE e.message ILIKE '%' || p || '%'))
  AND (cardinality(@message_not_like::text[]) = 0 OR NOT EXISTS (SELECT 1 FROM unnest(@message_not_like::text[]) p WHERE e.message ILIKE '%' || p || '%'))
  AND (cardinality(@message_prefix::text[]) = 0 OR EXISTS (SELECT 1 FROM unnest(@message_prefix::text[]) p WHERE e.message LIKE p || '%'))
  AND (cardinality(@trace_in::text[]) = 0 OR e.trace_id = ANY(@trace_in::text[]))
  AND (cardinality(@trace_not::text[]) = 0 OR NOT (e.trace_id = ANY(@trace_not::text[])))
  AND (@trace_exists::int = 0 OR (@trace_exists::int > 0) = (e.trace_id <> ''))
  AND (cardinality(@runtime_in::text[]) = 0 OR e.runtime_id = ANY(@runtime_in::text[]))
  AND (cardinality(@runtime_not::text[]) = 0 OR NOT (e.runtime_id = ANY(@runtime_not::text[])))
  AND (@runtime_exists::int = 0 OR (@runtime_exists::int > 0) = (e.runtime_id <> ''))
  AND (cardinality(@group_key_in::text[]) = 0 OR e.group_key = ANY(@group_key_in::text[]))
  AND (cardinality(@group_key_not::text[]) = 0 OR NOT (e.group_key = ANY(@group_key_not::text[])))
  AND (@group_key_exists::int = 0 OR (@group_key_exists::int > 0) = (e.group_key <> ''))
  AND (cardinality(@origin_in::text[]) = 0 OR e.origin = ANY(@origin_in::text[]))
  AND (cardinality(@origin_not::text[]) = 0 OR NOT (e.origin = ANY(@origin_not::text[])))
  AND (cardinality(@fingerprint_in::bytea[]) = 0 OR e.fingerprint = ANY(@fingerprint_in::bytea[]))
  AND (cardinality(@fingerprint_not::bytea[]) = 0 OR NOT (e.fingerprint = ANY(@fingerprint_not::bytea[])))
  AND (@handled::int = 0 OR (@handled::int > 0 AND e.handled IS TRUE) OR (@handled::int < 0 AND e.handled IS FALSE))
  AND (cardinality(@ingest_in::text[]) = 0 OR e.ingest = ANY(@ingest_in::text[]))
  AND (cardinality(@ingest_not::text[]) = 0 OR NOT (e.ingest = ANY(@ingest_not::text[])))
  AND (cardinality(@instance_in::text[]) = 0 OR e.instance = ANY(@instance_in::text[]))
  AND (cardinality(@instance_not::text[]) = 0 OR NOT (e.instance = ANY(@instance_not::text[])))
  AND (@text::text = '' OR e.message ILIKE '%' || @text::text || '%' OR e.type ILIKE '%' || @text::text || '%'
    OR (@text_in_stack::bool AND e.stacktrace ILIKE '%' || @text::text || '%'))
GROUP BY e.origin
ORDER BY count DESC, value
LIMIT @page_size;

-- name: ErrorFacetIngest :many
SELECT e.ingest::text AS value, count(*)::bigint AS count, max(e.time) AS last_seen
FROM backplane.app_error e
WHERE e.time >= @start AND e.time < @end
  AND (cardinality(@service_in::text[]) = 0 OR e.service = ANY(@service_in::text[]))
  AND (cardinality(@service_not::text[]) = 0 OR NOT (e.service = ANY(@service_not::text[])))
  AND (cardinality(@service_prefix::text[]) = 0 OR EXISTS (SELECT 1 FROM unnest(@service_prefix::text[]) p WHERE e.service LIKE p || '%'))
  AND (cardinality(@environment_in::text[]) = 0 OR e.environment = ANY(@environment_in::text[]))
  AND (cardinality(@environment_not::text[]) = 0 OR NOT (e.environment = ANY(@environment_not::text[])))
  AND (@environment_exists::int = 0 OR (@environment_exists::int > 0) = (e.environment <> ''))
  AND (cardinality(@release_in::text[]) = 0 OR e.release = ANY(@release_in::text[]))
  AND (cardinality(@release_not::text[]) = 0 OR NOT (e.release = ANY(@release_not::text[])))
  AND (cardinality(@release_prefix::text[]) = 0 OR EXISTS (SELECT 1 FROM unnest(@release_prefix::text[]) p WHERE e.release LIKE p || '%'))
  AND (@release_exists::int = 0 OR (@release_exists::int > 0) = (e.release <> ''))
  AND (cardinality(@type_in::text[]) = 0 OR e.type = ANY(@type_in::text[]))
  AND (cardinality(@type_not::text[]) = 0 OR NOT (e.type = ANY(@type_not::text[])))
  AND (cardinality(@type_like::text[]) = 0 OR EXISTS (SELECT 1 FROM unnest(@type_like::text[]) p WHERE e.type ILIKE '%' || p || '%'))
  AND (cardinality(@type_not_like::text[]) = 0 OR NOT EXISTS (SELECT 1 FROM unnest(@type_not_like::text[]) p WHERE e.type ILIKE '%' || p || '%'))
  AND (cardinality(@type_prefix::text[]) = 0 OR EXISTS (SELECT 1 FROM unnest(@type_prefix::text[]) p WHERE e.type LIKE p || '%'))
  AND (cardinality(@message_in::text[]) = 0 OR e.message = ANY(@message_in::text[]))
  AND (cardinality(@message_not::text[]) = 0 OR NOT (e.message = ANY(@message_not::text[])))
  AND (cardinality(@message_like::text[]) = 0 OR EXISTS (SELECT 1 FROM unnest(@message_like::text[]) p WHERE e.message ILIKE '%' || p || '%'))
  AND (cardinality(@message_not_like::text[]) = 0 OR NOT EXISTS (SELECT 1 FROM unnest(@message_not_like::text[]) p WHERE e.message ILIKE '%' || p || '%'))
  AND (cardinality(@message_prefix::text[]) = 0 OR EXISTS (SELECT 1 FROM unnest(@message_prefix::text[]) p WHERE e.message LIKE p || '%'))
  AND (cardinality(@trace_in::text[]) = 0 OR e.trace_id = ANY(@trace_in::text[]))
  AND (cardinality(@trace_not::text[]) = 0 OR NOT (e.trace_id = ANY(@trace_not::text[])))
  AND (@trace_exists::int = 0 OR (@trace_exists::int > 0) = (e.trace_id <> ''))
  AND (cardinality(@runtime_in::text[]) = 0 OR e.runtime_id = ANY(@runtime_in::text[]))
  AND (cardinality(@runtime_not::text[]) = 0 OR NOT (e.runtime_id = ANY(@runtime_not::text[])))
  AND (@runtime_exists::int = 0 OR (@runtime_exists::int > 0) = (e.runtime_id <> ''))
  AND (cardinality(@group_key_in::text[]) = 0 OR e.group_key = ANY(@group_key_in::text[]))
  AND (cardinality(@group_key_not::text[]) = 0 OR NOT (e.group_key = ANY(@group_key_not::text[])))
  AND (@group_key_exists::int = 0 OR (@group_key_exists::int > 0) = (e.group_key <> ''))
  AND (cardinality(@origin_in::text[]) = 0 OR e.origin = ANY(@origin_in::text[]))
  AND (cardinality(@origin_not::text[]) = 0 OR NOT (e.origin = ANY(@origin_not::text[])))
  AND (cardinality(@fingerprint_in::bytea[]) = 0 OR e.fingerprint = ANY(@fingerprint_in::bytea[]))
  AND (cardinality(@fingerprint_not::bytea[]) = 0 OR NOT (e.fingerprint = ANY(@fingerprint_not::bytea[])))
  AND (@handled::int = 0 OR (@handled::int > 0 AND e.handled IS TRUE) OR (@handled::int < 0 AND e.handled IS FALSE))
  AND (cardinality(@ingest_in::text[]) = 0 OR e.ingest = ANY(@ingest_in::text[]))
  AND (cardinality(@ingest_not::text[]) = 0 OR NOT (e.ingest = ANY(@ingest_not::text[])))
  AND (cardinality(@instance_in::text[]) = 0 OR e.instance = ANY(@instance_in::text[]))
  AND (cardinality(@instance_not::text[]) = 0 OR NOT (e.instance = ANY(@instance_not::text[])))
  AND (@text::text = '' OR e.message ILIKE '%' || @text::text || '%' OR e.type ILIKE '%' || @text::text || '%'
    OR (@text_in_stack::bool AND e.stacktrace ILIKE '%' || @text::text || '%'))
GROUP BY e.ingest
ORDER BY count DESC, value
LIMIT @page_size;

-- name: ErrorFacetInstance :many
SELECT e.instance::text AS value, count(*)::bigint AS count, max(e.time) AS last_seen
FROM backplane.app_error e
WHERE e.time >= @start AND e.time < @end
  AND (cardinality(@service_in::text[]) = 0 OR e.service = ANY(@service_in::text[]))
  AND (cardinality(@service_not::text[]) = 0 OR NOT (e.service = ANY(@service_not::text[])))
  AND (cardinality(@service_prefix::text[]) = 0 OR EXISTS (SELECT 1 FROM unnest(@service_prefix::text[]) p WHERE e.service LIKE p || '%'))
  AND (cardinality(@environment_in::text[]) = 0 OR e.environment = ANY(@environment_in::text[]))
  AND (cardinality(@environment_not::text[]) = 0 OR NOT (e.environment = ANY(@environment_not::text[])))
  AND (@environment_exists::int = 0 OR (@environment_exists::int > 0) = (e.environment <> ''))
  AND (cardinality(@release_in::text[]) = 0 OR e.release = ANY(@release_in::text[]))
  AND (cardinality(@release_not::text[]) = 0 OR NOT (e.release = ANY(@release_not::text[])))
  AND (cardinality(@release_prefix::text[]) = 0 OR EXISTS (SELECT 1 FROM unnest(@release_prefix::text[]) p WHERE e.release LIKE p || '%'))
  AND (@release_exists::int = 0 OR (@release_exists::int > 0) = (e.release <> ''))
  AND (cardinality(@type_in::text[]) = 0 OR e.type = ANY(@type_in::text[]))
  AND (cardinality(@type_not::text[]) = 0 OR NOT (e.type = ANY(@type_not::text[])))
  AND (cardinality(@type_like::text[]) = 0 OR EXISTS (SELECT 1 FROM unnest(@type_like::text[]) p WHERE e.type ILIKE '%' || p || '%'))
  AND (cardinality(@type_not_like::text[]) = 0 OR NOT EXISTS (SELECT 1 FROM unnest(@type_not_like::text[]) p WHERE e.type ILIKE '%' || p || '%'))
  AND (cardinality(@type_prefix::text[]) = 0 OR EXISTS (SELECT 1 FROM unnest(@type_prefix::text[]) p WHERE e.type LIKE p || '%'))
  AND (cardinality(@message_in::text[]) = 0 OR e.message = ANY(@message_in::text[]))
  AND (cardinality(@message_not::text[]) = 0 OR NOT (e.message = ANY(@message_not::text[])))
  AND (cardinality(@message_like::text[]) = 0 OR EXISTS (SELECT 1 FROM unnest(@message_like::text[]) p WHERE e.message ILIKE '%' || p || '%'))
  AND (cardinality(@message_not_like::text[]) = 0 OR NOT EXISTS (SELECT 1 FROM unnest(@message_not_like::text[]) p WHERE e.message ILIKE '%' || p || '%'))
  AND (cardinality(@message_prefix::text[]) = 0 OR EXISTS (SELECT 1 FROM unnest(@message_prefix::text[]) p WHERE e.message LIKE p || '%'))
  AND (cardinality(@trace_in::text[]) = 0 OR e.trace_id = ANY(@trace_in::text[]))
  AND (cardinality(@trace_not::text[]) = 0 OR NOT (e.trace_id = ANY(@trace_not::text[])))
  AND (@trace_exists::int = 0 OR (@trace_exists::int > 0) = (e.trace_id <> ''))
  AND (cardinality(@runtime_in::text[]) = 0 OR e.runtime_id = ANY(@runtime_in::text[]))
  AND (cardinality(@runtime_not::text[]) = 0 OR NOT (e.runtime_id = ANY(@runtime_not::text[])))
  AND (@runtime_exists::int = 0 OR (@runtime_exists::int > 0) = (e.runtime_id <> ''))
  AND (cardinality(@group_key_in::text[]) = 0 OR e.group_key = ANY(@group_key_in::text[]))
  AND (cardinality(@group_key_not::text[]) = 0 OR NOT (e.group_key = ANY(@group_key_not::text[])))
  AND (@group_key_exists::int = 0 OR (@group_key_exists::int > 0) = (e.group_key <> ''))
  AND (cardinality(@origin_in::text[]) = 0 OR e.origin = ANY(@origin_in::text[]))
  AND (cardinality(@origin_not::text[]) = 0 OR NOT (e.origin = ANY(@origin_not::text[])))
  AND (cardinality(@fingerprint_in::bytea[]) = 0 OR e.fingerprint = ANY(@fingerprint_in::bytea[]))
  AND (cardinality(@fingerprint_not::bytea[]) = 0 OR NOT (e.fingerprint = ANY(@fingerprint_not::bytea[])))
  AND (@handled::int = 0 OR (@handled::int > 0 AND e.handled IS TRUE) OR (@handled::int < 0 AND e.handled IS FALSE))
  AND (cardinality(@ingest_in::text[]) = 0 OR e.ingest = ANY(@ingest_in::text[]))
  AND (cardinality(@ingest_not::text[]) = 0 OR NOT (e.ingest = ANY(@ingest_not::text[])))
  AND (cardinality(@instance_in::text[]) = 0 OR e.instance = ANY(@instance_in::text[]))
  AND (cardinality(@instance_not::text[]) = 0 OR NOT (e.instance = ANY(@instance_not::text[])))
  AND (@text::text = '' OR e.message ILIKE '%' || @text::text || '%' OR e.type ILIKE '%' || @text::text || '%'
    OR (@text_in_stack::bool AND e.stacktrace ILIKE '%' || @text::text || '%'))
GROUP BY e.instance
ORDER BY count DESC, value
LIMIT @page_size;

-- name: ErrorFacetHandled :many
SELECT CASE WHEN e.handled IS TRUE THEN 'true' ELSE 'false' END::text AS value,
  count(*)::bigint AS count, max(e.time) AS last_seen
FROM backplane.app_error e
WHERE e.time >= @start AND e.time < @end AND e.handled IS NOT NULL
  AND (cardinality(@service_in::text[]) = 0 OR e.service = ANY(@service_in::text[]))
  AND (cardinality(@service_not::text[]) = 0 OR NOT (e.service = ANY(@service_not::text[])))
  AND (cardinality(@service_prefix::text[]) = 0 OR EXISTS (SELECT 1 FROM unnest(@service_prefix::text[]) p WHERE e.service LIKE p || '%'))
  AND (cardinality(@environment_in::text[]) = 0 OR e.environment = ANY(@environment_in::text[]))
  AND (cardinality(@environment_not::text[]) = 0 OR NOT (e.environment = ANY(@environment_not::text[])))
  AND (@environment_exists::int = 0 OR (@environment_exists::int > 0) = (e.environment <> ''))
  AND (cardinality(@release_in::text[]) = 0 OR e.release = ANY(@release_in::text[]))
  AND (cardinality(@release_not::text[]) = 0 OR NOT (e.release = ANY(@release_not::text[])))
  AND (cardinality(@release_prefix::text[]) = 0 OR EXISTS (SELECT 1 FROM unnest(@release_prefix::text[]) p WHERE e.release LIKE p || '%'))
  AND (@release_exists::int = 0 OR (@release_exists::int > 0) = (e.release <> ''))
  AND (cardinality(@type_in::text[]) = 0 OR e.type = ANY(@type_in::text[]))
  AND (cardinality(@type_not::text[]) = 0 OR NOT (e.type = ANY(@type_not::text[])))
  AND (cardinality(@type_like::text[]) = 0 OR EXISTS (SELECT 1 FROM unnest(@type_like::text[]) p WHERE e.type ILIKE '%' || p || '%'))
  AND (cardinality(@type_not_like::text[]) = 0 OR NOT EXISTS (SELECT 1 FROM unnest(@type_not_like::text[]) p WHERE e.type ILIKE '%' || p || '%'))
  AND (cardinality(@type_prefix::text[]) = 0 OR EXISTS (SELECT 1 FROM unnest(@type_prefix::text[]) p WHERE e.type LIKE p || '%'))
  AND (cardinality(@message_in::text[]) = 0 OR e.message = ANY(@message_in::text[]))
  AND (cardinality(@message_not::text[]) = 0 OR NOT (e.message = ANY(@message_not::text[])))
  AND (cardinality(@message_like::text[]) = 0 OR EXISTS (SELECT 1 FROM unnest(@message_like::text[]) p WHERE e.message ILIKE '%' || p || '%'))
  AND (cardinality(@message_not_like::text[]) = 0 OR NOT EXISTS (SELECT 1 FROM unnest(@message_not_like::text[]) p WHERE e.message ILIKE '%' || p || '%'))
  AND (cardinality(@message_prefix::text[]) = 0 OR EXISTS (SELECT 1 FROM unnest(@message_prefix::text[]) p WHERE e.message LIKE p || '%'))
  AND (cardinality(@trace_in::text[]) = 0 OR e.trace_id = ANY(@trace_in::text[]))
  AND (cardinality(@trace_not::text[]) = 0 OR NOT (e.trace_id = ANY(@trace_not::text[])))
  AND (@trace_exists::int = 0 OR (@trace_exists::int > 0) = (e.trace_id <> ''))
  AND (cardinality(@runtime_in::text[]) = 0 OR e.runtime_id = ANY(@runtime_in::text[]))
  AND (cardinality(@runtime_not::text[]) = 0 OR NOT (e.runtime_id = ANY(@runtime_not::text[])))
  AND (@runtime_exists::int = 0 OR (@runtime_exists::int > 0) = (e.runtime_id <> ''))
  AND (cardinality(@group_key_in::text[]) = 0 OR e.group_key = ANY(@group_key_in::text[]))
  AND (cardinality(@group_key_not::text[]) = 0 OR NOT (e.group_key = ANY(@group_key_not::text[])))
  AND (@group_key_exists::int = 0 OR (@group_key_exists::int > 0) = (e.group_key <> ''))
  AND (cardinality(@origin_in::text[]) = 0 OR e.origin = ANY(@origin_in::text[]))
  AND (cardinality(@origin_not::text[]) = 0 OR NOT (e.origin = ANY(@origin_not::text[])))
  AND (cardinality(@fingerprint_in::bytea[]) = 0 OR e.fingerprint = ANY(@fingerprint_in::bytea[]))
  AND (cardinality(@fingerprint_not::bytea[]) = 0 OR NOT (e.fingerprint = ANY(@fingerprint_not::bytea[])))
  AND (@handled::int = 0 OR (@handled::int > 0 AND e.handled IS TRUE) OR (@handled::int < 0 AND e.handled IS FALSE))
  AND (cardinality(@ingest_in::text[]) = 0 OR e.ingest = ANY(@ingest_in::text[]))
  AND (cardinality(@ingest_not::text[]) = 0 OR NOT (e.ingest = ANY(@ingest_not::text[])))
  AND (cardinality(@instance_in::text[]) = 0 OR e.instance = ANY(@instance_in::text[]))
  AND (cardinality(@instance_not::text[]) = 0 OR NOT (e.instance = ANY(@instance_not::text[])))
  AND (@text::text = '' OR e.message ILIKE '%' || @text::text || '%' OR e.type ILIKE '%' || @text::text || '%'
    OR (@text_in_stack::bool AND e.stacktrace ILIKE '%' || @text::text || '%'))
GROUP BY 1
ORDER BY count DESC, value
LIMIT @page_size;

-- name: SearchErrorGroups :many
-- Groups with stored occurrences in the range: counts over the range, the
-- group's totals, the newest occurrence and what was seen, sorted as asked
-- (1: last seen, 2: count in range, 3: first seen).
SELECT g.fingerprint, g.service, g.type, g.message, g.first_seen, g.last_seen, g.count, g.stored,
  g.state, g.state_at, g.state_by, g.state_note, g.regressed_at,
  r.count_in_range, r.newest_time, r.newest_id, r.any_sdk, r.any_unhandled, r.environments, r.releases
FROM (
  SELECT e.fingerprint, e.service, count(*)::bigint AS count_in_range, max(e.time) AS newest_time,
    ((array_agg(e.id ORDER BY e.time DESC, e.id DESC))[1])::uuid AS newest_id,
    bool_or(e.origin = 'sdk') AS any_sdk, bool_or(e.handled IS FALSE) AS any_unhandled,
    (SELECT array_agg(v ORDER BY c DESC, v) FROM (SELECT x.environment AS v, count(*) AS c FROM unnest(array_agg(e.environment)) AS x(environment)
      WHERE x.environment <> '' GROUP BY x.environment LIMIT 5) AS envs)::text[] AS environments,
    (SELECT array_agg(v ORDER BY c DESC, v) FROM (SELECT x.release AS v, count(*) AS c FROM unnest(array_agg(e.release)) AS x(release)
      WHERE x.release <> '' GROUP BY x.release LIMIT 5) AS rels)::text[] AS releases
  FROM backplane.app_error e
  WHERE e.time >= @start AND e.time < @end
  AND (cardinality(@service_in::text[]) = 0 OR e.service = ANY(@service_in::text[]))
  AND (cardinality(@service_not::text[]) = 0 OR NOT (e.service = ANY(@service_not::text[])))
  AND (cardinality(@service_prefix::text[]) = 0 OR EXISTS (SELECT 1 FROM unnest(@service_prefix::text[]) p WHERE e.service LIKE p || '%'))
  AND (cardinality(@environment_in::text[]) = 0 OR e.environment = ANY(@environment_in::text[]))
  AND (cardinality(@environment_not::text[]) = 0 OR NOT (e.environment = ANY(@environment_not::text[])))
  AND (@environment_exists::int = 0 OR (@environment_exists::int > 0) = (e.environment <> ''))
  AND (cardinality(@release_in::text[]) = 0 OR e.release = ANY(@release_in::text[]))
  AND (cardinality(@release_not::text[]) = 0 OR NOT (e.release = ANY(@release_not::text[])))
  AND (cardinality(@release_prefix::text[]) = 0 OR EXISTS (SELECT 1 FROM unnest(@release_prefix::text[]) p WHERE e.release LIKE p || '%'))
  AND (@release_exists::int = 0 OR (@release_exists::int > 0) = (e.release <> ''))
  AND (cardinality(@type_in::text[]) = 0 OR e.type = ANY(@type_in::text[]))
  AND (cardinality(@type_not::text[]) = 0 OR NOT (e.type = ANY(@type_not::text[])))
  AND (cardinality(@type_like::text[]) = 0 OR EXISTS (SELECT 1 FROM unnest(@type_like::text[]) p WHERE e.type ILIKE '%' || p || '%'))
  AND (cardinality(@type_not_like::text[]) = 0 OR NOT EXISTS (SELECT 1 FROM unnest(@type_not_like::text[]) p WHERE e.type ILIKE '%' || p || '%'))
  AND (cardinality(@type_prefix::text[]) = 0 OR EXISTS (SELECT 1 FROM unnest(@type_prefix::text[]) p WHERE e.type LIKE p || '%'))
  AND (cardinality(@message_in::text[]) = 0 OR e.message = ANY(@message_in::text[]))
  AND (cardinality(@message_not::text[]) = 0 OR NOT (e.message = ANY(@message_not::text[])))
  AND (cardinality(@message_like::text[]) = 0 OR EXISTS (SELECT 1 FROM unnest(@message_like::text[]) p WHERE e.message ILIKE '%' || p || '%'))
  AND (cardinality(@message_not_like::text[]) = 0 OR NOT EXISTS (SELECT 1 FROM unnest(@message_not_like::text[]) p WHERE e.message ILIKE '%' || p || '%'))
  AND (cardinality(@message_prefix::text[]) = 0 OR EXISTS (SELECT 1 FROM unnest(@message_prefix::text[]) p WHERE e.message LIKE p || '%'))
  AND (cardinality(@trace_in::text[]) = 0 OR e.trace_id = ANY(@trace_in::text[]))
  AND (cardinality(@trace_not::text[]) = 0 OR NOT (e.trace_id = ANY(@trace_not::text[])))
  AND (@trace_exists::int = 0 OR (@trace_exists::int > 0) = (e.trace_id <> ''))
  AND (cardinality(@runtime_in::text[]) = 0 OR e.runtime_id = ANY(@runtime_in::text[]))
  AND (cardinality(@runtime_not::text[]) = 0 OR NOT (e.runtime_id = ANY(@runtime_not::text[])))
  AND (@runtime_exists::int = 0 OR (@runtime_exists::int > 0) = (e.runtime_id <> ''))
  AND (cardinality(@group_key_in::text[]) = 0 OR e.group_key = ANY(@group_key_in::text[]))
  AND (cardinality(@group_key_not::text[]) = 0 OR NOT (e.group_key = ANY(@group_key_not::text[])))
  AND (@group_key_exists::int = 0 OR (@group_key_exists::int > 0) = (e.group_key <> ''))
  AND (cardinality(@origin_in::text[]) = 0 OR e.origin = ANY(@origin_in::text[]))
  AND (cardinality(@origin_not::text[]) = 0 OR NOT (e.origin = ANY(@origin_not::text[])))
  AND (cardinality(@fingerprint_in::bytea[]) = 0 OR e.fingerprint = ANY(@fingerprint_in::bytea[]))
  AND (cardinality(@fingerprint_not::bytea[]) = 0 OR NOT (e.fingerprint = ANY(@fingerprint_not::bytea[])))
  AND (@handled::int = 0 OR (@handled::int > 0 AND e.handled IS TRUE) OR (@handled::int < 0 AND e.handled IS FALSE))
  AND (cardinality(@ingest_in::text[]) = 0 OR e.ingest = ANY(@ingest_in::text[]))
  AND (cardinality(@ingest_not::text[]) = 0 OR NOT (e.ingest = ANY(@ingest_not::text[])))
  AND (cardinality(@instance_in::text[]) = 0 OR e.instance = ANY(@instance_in::text[]))
  AND (cardinality(@instance_not::text[]) = 0 OR NOT (e.instance = ANY(@instance_not::text[])))
  AND (@text::text = '' OR e.message ILIKE '%' || @text::text || '%' OR e.type ILIKE '%' || @text::text || '%'
    OR (@text_in_stack::bool AND e.stacktrace ILIKE '%' || @text::text || '%'))
  GROUP BY e.fingerprint, e.service
) r
JOIN backplane.app_error_group g ON g.fingerprint = r.fingerprint AND g.service = r.service
WHERE cardinality(@state_in::text[]) = 0 OR g.state = ANY(@state_in::text[])
ORDER BY
  CASE WHEN @sort::int = 2 THEN r.count_in_range END DESC,
  CASE WHEN @sort::int = 3 THEN g.first_seen END DESC,
  r.newest_time DESC, g.fingerprint DESC
LIMIT @page_size OFFSET @page_offset;

-- name: ErrorGroupTrends :many
-- Stored occurrences per bucket of the range for the given groups.
SELECT e.fingerprint, e.service, date_bin(@step::interval, e.time, @start::timestamptz)::timestamptz AS bucket, count(*)::bigint AS count
FROM backplane.app_error e
WHERE e.time >= @start AND e.time < @end
  AND (cardinality(@service_in::text[]) = 0 OR e.service = ANY(@service_in::text[]))
  AND (cardinality(@service_not::text[]) = 0 OR NOT (e.service = ANY(@service_not::text[])))
  AND (cardinality(@service_prefix::text[]) = 0 OR EXISTS (SELECT 1 FROM unnest(@service_prefix::text[]) p WHERE e.service LIKE p || '%'))
  AND (cardinality(@environment_in::text[]) = 0 OR e.environment = ANY(@environment_in::text[]))
  AND (cardinality(@environment_not::text[]) = 0 OR NOT (e.environment = ANY(@environment_not::text[])))
  AND (@environment_exists::int = 0 OR (@environment_exists::int > 0) = (e.environment <> ''))
  AND (cardinality(@release_in::text[]) = 0 OR e.release = ANY(@release_in::text[]))
  AND (cardinality(@release_not::text[]) = 0 OR NOT (e.release = ANY(@release_not::text[])))
  AND (cardinality(@release_prefix::text[]) = 0 OR EXISTS (SELECT 1 FROM unnest(@release_prefix::text[]) p WHERE e.release LIKE p || '%'))
  AND (@release_exists::int = 0 OR (@release_exists::int > 0) = (e.release <> ''))
  AND (cardinality(@type_in::text[]) = 0 OR e.type = ANY(@type_in::text[]))
  AND (cardinality(@type_not::text[]) = 0 OR NOT (e.type = ANY(@type_not::text[])))
  AND (cardinality(@type_like::text[]) = 0 OR EXISTS (SELECT 1 FROM unnest(@type_like::text[]) p WHERE e.type ILIKE '%' || p || '%'))
  AND (cardinality(@type_not_like::text[]) = 0 OR NOT EXISTS (SELECT 1 FROM unnest(@type_not_like::text[]) p WHERE e.type ILIKE '%' || p || '%'))
  AND (cardinality(@type_prefix::text[]) = 0 OR EXISTS (SELECT 1 FROM unnest(@type_prefix::text[]) p WHERE e.type LIKE p || '%'))
  AND (cardinality(@message_in::text[]) = 0 OR e.message = ANY(@message_in::text[]))
  AND (cardinality(@message_not::text[]) = 0 OR NOT (e.message = ANY(@message_not::text[])))
  AND (cardinality(@message_like::text[]) = 0 OR EXISTS (SELECT 1 FROM unnest(@message_like::text[]) p WHERE e.message ILIKE '%' || p || '%'))
  AND (cardinality(@message_not_like::text[]) = 0 OR NOT EXISTS (SELECT 1 FROM unnest(@message_not_like::text[]) p WHERE e.message ILIKE '%' || p || '%'))
  AND (cardinality(@message_prefix::text[]) = 0 OR EXISTS (SELECT 1 FROM unnest(@message_prefix::text[]) p WHERE e.message LIKE p || '%'))
  AND (cardinality(@trace_in::text[]) = 0 OR e.trace_id = ANY(@trace_in::text[]))
  AND (cardinality(@trace_not::text[]) = 0 OR NOT (e.trace_id = ANY(@trace_not::text[])))
  AND (@trace_exists::int = 0 OR (@trace_exists::int > 0) = (e.trace_id <> ''))
  AND (cardinality(@runtime_in::text[]) = 0 OR e.runtime_id = ANY(@runtime_in::text[]))
  AND (cardinality(@runtime_not::text[]) = 0 OR NOT (e.runtime_id = ANY(@runtime_not::text[])))
  AND (@runtime_exists::int = 0 OR (@runtime_exists::int > 0) = (e.runtime_id <> ''))
  AND (cardinality(@group_key_in::text[]) = 0 OR e.group_key = ANY(@group_key_in::text[]))
  AND (cardinality(@group_key_not::text[]) = 0 OR NOT (e.group_key = ANY(@group_key_not::text[])))
  AND (@group_key_exists::int = 0 OR (@group_key_exists::int > 0) = (e.group_key <> ''))
  AND (cardinality(@origin_in::text[]) = 0 OR e.origin = ANY(@origin_in::text[]))
  AND (cardinality(@origin_not::text[]) = 0 OR NOT (e.origin = ANY(@origin_not::text[])))
  AND (cardinality(@fingerprint_in::bytea[]) = 0 OR e.fingerprint = ANY(@fingerprint_in::bytea[]))
  AND (cardinality(@fingerprint_not::bytea[]) = 0 OR NOT (e.fingerprint = ANY(@fingerprint_not::bytea[])))
  AND (@handled::int = 0 OR (@handled::int > 0 AND e.handled IS TRUE) OR (@handled::int < 0 AND e.handled IS FALSE))
  AND (cardinality(@ingest_in::text[]) = 0 OR e.ingest = ANY(@ingest_in::text[]))
  AND (cardinality(@ingest_not::text[]) = 0 OR NOT (e.ingest = ANY(@ingest_not::text[])))
  AND (cardinality(@instance_in::text[]) = 0 OR e.instance = ANY(@instance_in::text[]))
  AND (cardinality(@instance_not::text[]) = 0 OR NOT (e.instance = ANY(@instance_not::text[])))
  AND (@text::text = '' OR e.message ILIKE '%' || @text::text || '%' OR e.type ILIKE '%' || @text::text || '%'
    OR (@text_in_stack::bool AND e.stacktrace ILIKE '%' || @text::text || '%'))
  AND e.fingerprint = ANY(@fingerprints::bytea[])
GROUP BY e.fingerprint, e.service, bucket
ORDER BY e.fingerprint, e.service, bucket;
