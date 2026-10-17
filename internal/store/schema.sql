-- Declarative schema of backplane: the desired state of schema `backplane`.
-- Edit this file, then `make db-migration name=<what>` writes the migration
-- that takes the history to it, and `make db` regenerates the queries.
-- Every object is schema-qualified.

CREATE SCHEMA backplane;

-- The installation this database belongs to: one row, created by the first
-- backplane to start; replicas and restarts find it.
CREATE TABLE backplane.installation (
  id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
  singleton  boolean     NOT NULL DEFAULT true UNIQUE CHECK (singleton),
  created_at timestamptz NOT NULL DEFAULT now()
);

-- Live-value overrides of a service (§5.2): one row per revision, append-only;
-- revisions count from 1 per service, a rollback is a new revision copying an
-- older one's values (rollback_of). overrides: Live path ("greeter.suffix")
-- -> JSON value, as saved; kv: Live path -> the exact string delivered to
-- Consul KV config/<service>/<path with '/'> (§5.3).
CREATE TABLE backplane.config_revision (
  service     text        NOT NULL,
  revision    bigint      NOT NULL CHECK (revision > 0),
  overrides   jsonb       NOT NULL,
  kv          jsonb       NOT NULL,
  author      text        NOT NULL,
  comment     text        NOT NULL DEFAULT '',
  created_at  timestamptz NOT NULL DEFAULT now(),
  rollback_of bigint,
  PRIMARY KEY (service, revision),
  FOREIGN KEY (service, rollback_of) REFERENCES backplane.config_revision (service, revision)
);

-- The revision of each service that Consul KV must hold: the latest one.
CREATE TABLE backplane.config_current (
  service    text        PRIMARY KEY,
  revision   bigint      NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (service, revision) REFERENCES backplane.config_revision (service, revision)
);

-- Console sessions (§11.3). The cookie carries a random token; only its
-- SHA-256 is stored. id is the public handle (list, revoke).
CREATE TABLE backplane.console_session (
  id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
  token_hash   bytea       NOT NULL UNIQUE,
  created_at   timestamptz NOT NULL,
  expires_at   timestamptz NOT NULL,
  last_seen_at timestamptz NOT NULL,
  address      text        NOT NULL,
  user_agent   text        NOT NULL
);

CREATE INDEX console_session_expires_at_idx ON backplane.console_session (expires_at);

-- Shared login protection for every platform replica. Expired address rows
-- are deleted in bounded batches; timestamps come from PostgreSQL.
CREATE TABLE backplane.console_login_state (
  singleton     boolean PRIMARY KEY DEFAULT true CHECK (singleton),
  failures      bigint NOT NULL DEFAULT 0 CHECK (failures >= 0),
  forget_at     timestamptz NOT NULL DEFAULT 'epoch',
  blocked_until timestamptz NOT NULL DEFAULT 'epoch'
);

CREATE TABLE backplane.console_login_attempt (
  address    text PRIMARY KEY,
  attempts   integer NOT NULL CHECK (attempts > 0),
  expires_at timestamptz NOT NULL
);

CREATE INDEX console_login_attempt_expires_at_idx ON backplane.console_login_attempt (expires_at);

-- Bindings (§7.1): versions of the binding of each hook ("<service>.<Hook>"),
-- append-only; versions count from 1 per hook, a rollback is a new version
-- copying an older one's definition (rollback_of), a delete is a tombstone
-- (definition NULL). definition: protojson of backplane.console.v1.BindingDefinition
-- (steps keyed by name, values as JSON trees of CEL expressions).
-- managed_by: the wiring file (BACKPLANE_WIRING_DIR) the version came from;
-- while the current version has one, the console cannot change the binding.
CREATE TABLE backplane.binding_version (
  hook        text        NOT NULL,
  version     bigint      NOT NULL CHECK (version > 0),
  definition  jsonb,
  author      text        NOT NULL,
  comment     text        NOT NULL DEFAULT '',
  created_at  timestamptz NOT NULL DEFAULT now(),
  rollback_of bigint,
  managed_by  text,
  PRIMARY KEY (hook, version),
  FOREIGN KEY (hook, rollback_of) REFERENCES backplane.binding_version (hook, version)
);

-- The version of each hook's binding in force: the latest one.
CREATE TABLE backplane.binding_current (
  hook       text        PRIMARY KEY,
  version    bigint      NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (hook, version) REFERENCES backplane.binding_version (hook, version)
);

-- Rules (§8.1): event -> steps. The row is the rule's identity and its
-- pause, which is not versioned.
CREATE TABLE backplane.rule (
  id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
  paused     boolean     NOT NULL DEFAULT false,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

-- Versions of a rule, as binding_version: name and definition (protojson of
-- backplane.console.v1.RuleDefinition; NULL: the tombstone of a delete),
-- managed_by as binding_version's.
CREATE TABLE backplane.rule_version (
  rule_id     uuid        NOT NULL REFERENCES backplane.rule (id),
  version     bigint      NOT NULL CHECK (version > 0),
  name        text        NOT NULL,
  definition  jsonb,
  author      text        NOT NULL,
  comment     text        NOT NULL DEFAULT '',
  created_at  timestamptz NOT NULL DEFAULT now(),
  rollback_of bigint,
  managed_by  text,
  PRIMARY KEY (rule_id, version),
  FOREIGN KEY (rule_id, rollback_of) REFERENCES backplane.rule_version (rule_id, version)
);

-- The version of each rule in force: the latest one.
CREATE TABLE backplane.rule_current (
  rule_id    uuid        PRIMARY KEY REFERENCES backplane.rule (id),
  version    bigint      NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (rule_id, version) REFERENCES backplane.rule_version (rule_id, version)
);

-- Updating this singleton locks audit sequence allocation until transaction
-- commit. A committed higher sequence can never overtake an uncommitted lower
-- one. retained_after records the expired prefix for resumable watch cursors.
CREATE TABLE backplane.audit_clock (
  singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
  sequence bigint NOT NULL DEFAULT 0 CHECK (sequence >= 0),
  retained_after bigint NOT NULL DEFAULT 0 CHECK (retained_after >= 0)
);

CREATE TABLE backplane.audit_entry (
  sequence bigint PRIMARY KEY CHECK (sequence > 0),
  id uuid NOT NULL UNIQUE,
  created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  actor text NOT NULL,
  action text NOT NULL,
  subject text NOT NULL,
  outcome text NOT NULL,
  operation_id uuid NOT NULL,
  detail jsonb NOT NULL DEFAULT '{}',
  -- The service the entry is about (its configuration, the binding of one of
  -- its hooks, a rule on one of its events, a command addressed to it); empty
  -- for installation-wide entries such as sessions.
  service text NOT NULL DEFAULT ''
);

CREATE INDEX audit_entry_created_at_idx ON backplane.audit_entry (created_at);
CREATE INDEX audit_entry_service_idx ON backplane.audit_entry (service, sequence);
CREATE INDEX audit_entry_operation_id_idx ON backplane.audit_entry (operation_id);
-- The audit feed filters a platform entry's detail as its attributes.
CREATE INDEX audit_entry_detail_idx ON backplane.audit_entry USING gin (detail jsonb_path_ops);

-- Entries and delivery work are inserted with the control mutation. Leases
-- fence acknowledgments after a crash; event delivery remains at least once.
CREATE TABLE backplane.audit_outbox (
  sequence bigint PRIMARY KEY REFERENCES backplane.audit_entry (sequence),
  available_at timestamptz NOT NULL DEFAULT now(),
  lease uuid,
  leased_until timestamptz NOT NULL DEFAULT '-infinity',
  attempts bigint NOT NULL DEFAULT 0,
  published_at timestamptz
);

CREATE INDEX audit_outbox_pending_idx ON backplane.audit_outbox (available_at, leased_until)
WHERE published_at IS NULL;

-- Application audit: OTLP log records marked backplane.audit=true that the
-- deployment's Collector forwards (services and third parties alike). Kept
-- until deleted by hand: backplane expires none. key deduplicates the
-- Collector's retries: backplane.audit.id when the record has one, else a
-- hash of the record and its resource.
CREATE TABLE backplane.app_audit (
  id uuid PRIMARY KEY,
  key bytea NOT NULL UNIQUE,
  -- The record's time, else its observed time, else received_at.
  time timestamptz NOT NULL,
  received_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  -- Resource service.name.
  service text NOT NULL,
  -- event.name, else the record's event_name, else its body (cut).
  action text NOT NULL,
  actor text NOT NULL DEFAULT '',
  subject text NOT NULL DEFAULT '',
  outcome text NOT NULL DEFAULT '',
  severity text NOT NULL DEFAULT '',
  body text NOT NULL DEFAULT '',
  attributes jsonb NOT NULL DEFAULT '{}',
  resource jsonb NOT NULL DEFAULT '{}',
  trace_id text NOT NULL DEFAULT '',
  span_id text NOT NULL DEFAULT ''
);

CREATE INDEX app_audit_time_idx ON backplane.app_audit (time, id);
CREATE INDEX app_audit_service_idx ON backplane.app_audit (service, time);
CREATE INDEX app_audit_action_idx ON backplane.app_audit (action, time);
CREATE INDEX app_audit_actor_idx ON backplane.app_audit (actor, time);
CREATE INDEX app_audit_subject_idx ON backplane.app_audit (subject, time);
CREATE INDEX app_audit_attributes_idx ON backplane.app_audit USING gin (attributes jsonb_path_ops);


-- The audit feed: platform entries and application records alike, a
-- platform entry's detail as its attributes.
CREATE VIEW backplane.audit_feed AS
  SELECT 'platform'::text AS source, e.id, e.created_at AS time, e.created_at AS received_at, e.service, e.actor,
    e.action, e.subject, e.outcome, ''::text AS message, e.operation_id::text AS operation, e.sequence,
    e.detail AS attributes, '{}'::jsonb AS resource, ''::text AS severity, ''::text AS trace_id, ''::text AS span_id
  FROM backplane.audit_entry e
  UNION ALL
  SELECT 'application'::text, a.id, a.time, a.received_at, a.service, a.actor, a.action, a.subject, a.outcome,
    a.body, ''::text, 0::bigint, a.attributes, a.resource, a.severity, a.trace_id, a.span_id
  FROM backplane.app_audit a;

-- Runs of the direct runtime (internal/direct): bindings and rules executed
-- in backplane's own process, without Temporal. The rows are the console's
-- view of a run — payloads are stored masked by the program's schemas, so
-- no secret lands here, as json so their text shows as written — and the
-- dedup of a rule's event (one run per
-- event id; a failed run may be claimed again by a redelivery). Retention
-- is deployment-controlled (direct.retention).
-- The programs direct runs ran, by content: a run keeps the definition it
-- ran (whatever the binding says now) without carrying its own copy.
CREATE TABLE backplane.direct_program (
  -- hex SHA-256 of spec
  hash       text        PRIMARY KEY,
  -- The marshaled bindings.Program.
  spec       json        NOT NULL,
  created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

CREATE TABLE backplane.direct_run (
  id         text        PRIMARY KEY,
  -- binding | rule
  kind       text        NOT NULL,
  -- The hook ("<service>.<Hook>") or the rule id.
  source     text        NOT NULL,
  version    bigint      NOT NULL DEFAULT 0,
  test       boolean     NOT NULL DEFAULT false,
  -- Who raised it: a service instance, a console session, "rule:<id>".
  caller     text        NOT NULL DEFAULT '',
  -- running | completed | failed | canceled
  status     text        NOT NULL,
  attempt    integer     NOT NULL DEFAULT 1 CHECK (attempt > 0),
  input      json,
  result     json,
  error      text        NOT NULL DEFAULT '',
  error_type text        NOT NULL DEFAULT '',
  program_hash text      NOT NULL REFERENCES backplane.direct_program (hash),
  -- The record is not the whole run: step records were lost to a full
  -- write queue, or the run's end was never recorded and the sweeper
  -- closed it.
  incomplete boolean     NOT NULL DEFAULT false,
  started_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  closed_at  timestamptz
);

CREATE INDEX direct_run_source_idx ON backplane.direct_run (kind, source, test, started_at DESC, id DESC);
CREATE INDEX direct_run_started_at_idx ON backplane.direct_run (started_at);

-- One call of a run: a step, an item of a for-each step or a body step
-- of one, by its call label ("send", "each[2]", "users[1].send").
CREATE TABLE backplane.direct_run_step (
  run_id       text        NOT NULL REFERENCES backplane.direct_run (id) ON DELETE CASCADE,
  label        text        NOT NULL,
  -- Scheduling order within the run.
  seq          integer     NOT NULL,
  step         text        NOT NULL,
  parent       text        NOT NULL DEFAULT '',
  item         integer,
  activity     text        NOT NULL,
  -- scheduled | started | completed | failed | timed_out | canceled
  status       text        NOT NULL,
  input        json,
  output       json,
  error        text        NOT NULL DEFAULT '',
  error_type   text        NOT NULL DEFAULT '',
  scheduled_at timestamptz NOT NULL,
  closed_at    timestamptz,
  PRIMARY KEY (run_id, label)
);

-- Application errors (internal/apperrors): every exception record the
-- deployment's Collector forwards to backplane's listener, kept whole —
-- the exception, the record's attributes, its resource and the browser
-- SDK's envelope (state and history) — for errors.retention. A group is
-- one fingerprint (type, normalized message, top frames; the SDK's group
-- key when it sends one) in one service: it counts every occurrence, also
-- those the burst limits did not store.
CREATE TABLE backplane.app_error_group (
  fingerprint  bytea       NOT NULL,
  service      text        NOT NULL,
  type         text        NOT NULL DEFAULT '',
  message      text        NOT NULL DEFAULT '',
  first_seen   timestamptz NOT NULL,
  last_seen    timestamptz NOT NULL,
  count        bigint      NOT NULL DEFAULT 0,
  stored       bigint      NOT NULL DEFAULT 0,
  -- Burst windows: occurrences seen in the current minute and day.
  minute_start timestamptz NOT NULL,
  minute_count integer     NOT NULL DEFAULT 0,
  day_start    timestamptz NOT NULL,
  day_count    integer     NOT NULL DEFAULT 0,
  -- Triage: open, resolved or muted, by whom and why; a resolved group
  -- reopens on an occurrence after its resolution and remembers when.
  state        text        NOT NULL DEFAULT 'open',
  state_at     timestamptz,
  state_by     text        NOT NULL DEFAULT '',
  state_note   text        NOT NULL DEFAULT '',
  regressed_at timestamptz,
  PRIMARY KEY (fingerprint, service),
  CHECK (state IN ('open', 'resolved', 'muted'))
);

CREATE INDEX app_error_group_last_seen_idx ON backplane.app_error_group (last_seen);
CREATE INDEX app_error_group_service_idx ON backplane.app_error_group (service, last_seen);

CREATE TABLE backplane.app_error (
  id            uuid        PRIMARY KEY,
  -- Deduplicates the Collector's retries: the SDK's event id, else a hash
  -- of the record and its resource.
  key           bytea       NOT NULL UNIQUE,
  fingerprint   bytea       NOT NULL,
  service       text        NOT NULL,
  time          timestamptz NOT NULL,
  received_at   timestamptz NOT NULL DEFAULT clock_timestamp(),
  environment   text        NOT NULL DEFAULT '',
  release       text        NOT NULL DEFAULT '',
  instance      text        NOT NULL DEFAULT '',
  -- sdk | otel_log
  origin        text        NOT NULL,
  -- proxy (the public OTLP ingest: browsers) | trusted (the Collector's own senders)
  ingest        text        NOT NULL DEFAULT '',
  event_id      text        NOT NULL DEFAULT '',
  type          text        NOT NULL DEFAULT '',
  message       text        NOT NULL DEFAULT '',
  stacktrace    text        NOT NULL DEFAULT '',
  severity      integer     NOT NULL DEFAULT 0,
  severity_text text        NOT NULL DEFAULT '',
  event_name    text        NOT NULL DEFAULT '',
  mechanism     text        NOT NULL DEFAULT '',
  -- NULL: the record does not say.
  handled       boolean,
  group_key     text        NOT NULL DEFAULT '',
  trace_id      text        NOT NULL DEFAULT '',
  span_id       text        NOT NULL DEFAULT '',
  runtime_id    text        NOT NULL DEFAULT '',
  scope         text        NOT NULL DEFAULT '',
  -- The record's body: the SDK's envelope as sent, or {"text": ...}.
  body          jsonb       NOT NULL DEFAULT 'null',
  attributes    jsonb       NOT NULL DEFAULT '{}',
  resource      jsonb       NOT NULL DEFAULT '{}',
  truncated     boolean     NOT NULL DEFAULT false,
  FOREIGN KEY (fingerprint, service) REFERENCES backplane.app_error_group (fingerprint, service) ON DELETE CASCADE
);

CREATE INDEX app_error_time_idx ON backplane.app_error (time DESC, id DESC);
CREATE INDEX app_error_service_idx ON backplane.app_error (service, time DESC);
CREATE INDEX app_error_group_idx ON backplane.app_error (fingerprint, service, time DESC);
CREATE INDEX app_error_trace_idx ON backplane.app_error (trace_id) WHERE trace_id <> '';
CREATE INDEX app_error_runtime_idx ON backplane.app_error (runtime_id) WHERE runtime_id <> '';
CREATE INDEX app_error_type_idx ON backplane.app_error (type, time DESC);
CREATE INDEX app_error_attributes_idx ON backplane.app_error USING gin (attributes jsonb_path_ops);
