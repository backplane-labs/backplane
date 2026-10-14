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
CREATE TABLE backplane.binding_version (
  hook        text        NOT NULL,
  version     bigint      NOT NULL CHECK (version > 0),
  definition  jsonb,
  author      text        NOT NULL,
  comment     text        NOT NULL DEFAULT '',
  created_at  timestamptz NOT NULL DEFAULT now(),
  rollback_of bigint,
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
-- backplane.console.v1.RuleDefinition; NULL: the tombstone of a delete).
CREATE TABLE backplane.rule_version (
  rule_id     uuid        NOT NULL REFERENCES backplane.rule (id),
  version     bigint      NOT NULL CHECK (version > 0),
  name        text        NOT NULL,
  definition  jsonb,
  author      text        NOT NULL,
  comment     text        NOT NULL DEFAULT '',
  created_at  timestamptz NOT NULL DEFAULT now(),
  rollback_of bigint,
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
