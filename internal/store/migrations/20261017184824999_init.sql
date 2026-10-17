-- sqld:up
CREATE SCHEMA IF NOT EXISTS "backplane";
CREATE TABLE "backplane"."app_audit" (
  "id" uuid NOT NULL,
  "key" bytea NOT NULL,
  "time" timestamptz NOT NULL,
  "received_at" timestamptz NOT NULL DEFAULT clock_timestamp(),
  "service" text NOT NULL,
  "action" text NOT NULL,
  "actor" text NOT NULL DEFAULT ''::text,
  "subject" text NOT NULL DEFAULT ''::text,
  "outcome" text NOT NULL DEFAULT ''::text,
  "severity" text NOT NULL DEFAULT ''::text,
  "body" text NOT NULL DEFAULT ''::text,
  "attributes" jsonb NOT NULL DEFAULT '{}'::jsonb,
  "resource" jsonb NOT NULL DEFAULT '{}'::jsonb,
  "trace_id" text NOT NULL DEFAULT ''::text,
  "span_id" text NOT NULL DEFAULT ''::text,
  PRIMARY KEY ("id")
);
CREATE TABLE "backplane"."app_error" (
  "id" uuid NOT NULL,
  "key" bytea NOT NULL,
  "fingerprint" bytea NOT NULL,
  "service" text NOT NULL,
  "time" timestamptz NOT NULL,
  "received_at" timestamptz NOT NULL DEFAULT clock_timestamp(),
  "environment" text NOT NULL DEFAULT ''::text,
  "release" text NOT NULL DEFAULT ''::text,
  "instance" text NOT NULL DEFAULT ''::text,
  "origin" text NOT NULL,
  "ingest" text NOT NULL DEFAULT ''::text,
  "event_id" text NOT NULL DEFAULT ''::text,
  "type" text NOT NULL DEFAULT ''::text,
  "message" text NOT NULL DEFAULT ''::text,
  "stacktrace" text NOT NULL DEFAULT ''::text,
  "severity" int4 NOT NULL DEFAULT 0,
  "severity_text" text NOT NULL DEFAULT ''::text,
  "event_name" text NOT NULL DEFAULT ''::text,
  "mechanism" text NOT NULL DEFAULT ''::text,
  "handled" bool,
  "group_key" text NOT NULL DEFAULT ''::text,
  "trace_id" text NOT NULL DEFAULT ''::text,
  "span_id" text NOT NULL DEFAULT ''::text,
  "runtime_id" text NOT NULL DEFAULT ''::text,
  "scope" text NOT NULL DEFAULT ''::text,
  "body" jsonb NOT NULL DEFAULT 'null'::jsonb,
  "attributes" jsonb NOT NULL DEFAULT '{}'::jsonb,
  "resource" jsonb NOT NULL DEFAULT '{}'::jsonb,
  "truncated" bool NOT NULL DEFAULT false,
  PRIMARY KEY ("id")
);
CREATE TABLE "backplane"."app_error_group" (
  "fingerprint" bytea NOT NULL,
  "service" text NOT NULL,
  "type" text NOT NULL DEFAULT ''::text,
  "message" text NOT NULL DEFAULT ''::text,
  "first_seen" timestamptz NOT NULL,
  "last_seen" timestamptz NOT NULL,
  "count" int8 NOT NULL DEFAULT 0,
  "stored" int8 NOT NULL DEFAULT 0,
  "minute_start" timestamptz NOT NULL,
  "minute_count" int4 NOT NULL DEFAULT 0,
  "day_start" timestamptz NOT NULL,
  "day_count" int4 NOT NULL DEFAULT 0,
  "state" text NOT NULL DEFAULT 'open'::text,
  "state_at" timestamptz,
  "state_by" text NOT NULL DEFAULT ''::text,
  "state_note" text NOT NULL DEFAULT ''::text,
  "regressed_at" timestamptz,
  PRIMARY KEY ("fingerprint", "service")
);
CREATE TABLE "backplane"."audit_clock" (
  "singleton" bool NOT NULL DEFAULT true,
  "sequence" int8 NOT NULL DEFAULT 0,
  "retained_after" int8 NOT NULL DEFAULT 0,
  PRIMARY KEY ("singleton")
);
CREATE TABLE "backplane"."audit_entry" (
  "sequence" int8 NOT NULL,
  "id" uuid NOT NULL,
  "created_at" timestamptz NOT NULL DEFAULT clock_timestamp(),
  "actor" text NOT NULL,
  "action" text NOT NULL,
  "subject" text NOT NULL,
  "outcome" text NOT NULL,
  "operation_id" uuid NOT NULL,
  "detail" jsonb NOT NULL DEFAULT '{}'::jsonb,
  "service" text NOT NULL DEFAULT ''::text,
  PRIMARY KEY ("sequence")
);
CREATE TABLE "backplane"."audit_outbox" (
  "sequence" int8 NOT NULL,
  "available_at" timestamptz NOT NULL DEFAULT now(),
  "lease" uuid,
  "leased_until" timestamptz NOT NULL DEFAULT '-infinity'::timestamp with time zone,
  "attempts" int8 NOT NULL DEFAULT 0,
  "published_at" timestamptz,
  PRIMARY KEY ("sequence")
);
CREATE TABLE "backplane"."binding_current" (
  "hook" text NOT NULL,
  "version" int8 NOT NULL,
  "updated_at" timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY ("hook")
);
CREATE TABLE "backplane"."binding_version" (
  "hook" text NOT NULL,
  "version" int8 NOT NULL,
  "definition" jsonb,
  "author" text NOT NULL,
  "comment" text NOT NULL DEFAULT ''::text,
  "created_at" timestamptz NOT NULL DEFAULT now(),
  "rollback_of" int8,
  "managed_by" text,
  PRIMARY KEY ("hook", "version")
);
CREATE TABLE "backplane"."config_current" (
  "service" text NOT NULL,
  "revision" int8 NOT NULL,
  "updated_at" timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY ("service")
);
CREATE TABLE "backplane"."config_revision" (
  "service" text NOT NULL,
  "revision" int8 NOT NULL,
  "overrides" jsonb NOT NULL,
  "kv" jsonb NOT NULL,
  "author" text NOT NULL,
  "comment" text NOT NULL DEFAULT ''::text,
  "created_at" timestamptz NOT NULL DEFAULT now(),
  "rollback_of" int8,
  PRIMARY KEY ("service", "revision")
);
CREATE TABLE "backplane"."console_login_attempt" (
  "address" text NOT NULL,
  "attempts" int4 NOT NULL,
  "expires_at" timestamptz NOT NULL,
  PRIMARY KEY ("address")
);
CREATE TABLE "backplane"."console_login_state" (
  "singleton" bool NOT NULL DEFAULT true,
  "failures" int8 NOT NULL DEFAULT 0,
  "forget_at" timestamptz NOT NULL DEFAULT '1970-01-01 00:00:00+00'::timestamp with time zone,
  "blocked_until" timestamptz NOT NULL DEFAULT '1970-01-01 00:00:00+00'::timestamp with time zone,
  PRIMARY KEY ("singleton")
);
CREATE TABLE "backplane"."console_session" (
  "id" uuid NOT NULL DEFAULT gen_random_uuid(),
  "token_hash" bytea NOT NULL,
  "created_at" timestamptz NOT NULL,
  "expires_at" timestamptz NOT NULL,
  "last_seen_at" timestamptz NOT NULL,
  "address" text NOT NULL,
  "user_agent" text NOT NULL,
  PRIMARY KEY ("id")
);
CREATE TABLE "backplane"."direct_program" (
  "hash" text NOT NULL,
  "spec" json NOT NULL,
  "created_at" timestamptz NOT NULL DEFAULT clock_timestamp(),
  PRIMARY KEY ("hash")
);
CREATE TABLE "backplane"."direct_run" (
  "id" text NOT NULL,
  "kind" text NOT NULL,
  "source" text NOT NULL,
  "version" int8 NOT NULL DEFAULT 0,
  "test" bool NOT NULL DEFAULT false,
  "caller" text NOT NULL DEFAULT ''::text,
  "status" text NOT NULL,
  "attempt" int4 NOT NULL DEFAULT 1,
  "input" json,
  "result" json,
  "error" text NOT NULL DEFAULT ''::text,
  "error_type" text NOT NULL DEFAULT ''::text,
  "program_hash" text NOT NULL,
  "incomplete" bool NOT NULL DEFAULT false,
  "started_at" timestamptz NOT NULL DEFAULT clock_timestamp(),
  "closed_at" timestamptz,
  PRIMARY KEY ("id")
);
CREATE TABLE "backplane"."direct_run_step" (
  "run_id" text NOT NULL,
  "label" text NOT NULL,
  "seq" int4 NOT NULL,
  "step" text NOT NULL,
  "parent" text NOT NULL DEFAULT ''::text,
  "item" int4,
  "activity" text NOT NULL,
  "status" text NOT NULL,
  "input" json,
  "output" json,
  "error" text NOT NULL DEFAULT ''::text,
  "error_type" text NOT NULL DEFAULT ''::text,
  "scheduled_at" timestamptz NOT NULL,
  "closed_at" timestamptz,
  PRIMARY KEY ("run_id", "label")
);
CREATE TABLE "backplane"."installation" (
  "id" uuid NOT NULL DEFAULT gen_random_uuid(),
  "singleton" bool NOT NULL DEFAULT true,
  "created_at" timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY ("id")
);
CREATE TABLE "backplane"."rule" (
  "id" uuid NOT NULL DEFAULT gen_random_uuid(),
  "paused" bool NOT NULL DEFAULT false,
  "created_at" timestamptz NOT NULL DEFAULT now(),
  "updated_at" timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY ("id")
);
CREATE TABLE "backplane"."rule_current" (
  "rule_id" uuid NOT NULL,
  "version" int8 NOT NULL,
  "updated_at" timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY ("rule_id")
);
CREATE TABLE "backplane"."rule_version" (
  "rule_id" uuid NOT NULL,
  "version" int8 NOT NULL,
  "name" text NOT NULL,
  "definition" jsonb,
  "author" text NOT NULL,
  "comment" text NOT NULL DEFAULT ''::text,
  "created_at" timestamptz NOT NULL DEFAULT now(),
  "rollback_of" int8,
  "managed_by" text,
  PRIMARY KEY ("rule_id", "version")
);
ALTER TABLE "backplane"."app_audit" ADD CONSTRAINT "app_audit_key_key" UNIQUE ("key");
ALTER TABLE "backplane"."app_error" ADD CONSTRAINT "app_error_key_key" UNIQUE ("key");
ALTER TABLE "backplane"."app_error_group" ADD CONSTRAINT "app_error_group_state_check" CHECK (state = ANY (ARRAY['open'::text, 'resolved'::text, 'muted'::text]));
ALTER TABLE "backplane"."audit_clock" ADD CONSTRAINT "audit_clock_retained_after_check" CHECK (retained_after >= 0);
ALTER TABLE "backplane"."audit_clock" ADD CONSTRAINT "audit_clock_sequence_check" CHECK (sequence >= 0);
ALTER TABLE "backplane"."audit_clock" ADD CONSTRAINT "audit_clock_singleton_check" CHECK (singleton);
ALTER TABLE "backplane"."audit_entry" ADD CONSTRAINT "audit_entry_id_key" UNIQUE ("id");
ALTER TABLE "backplane"."audit_entry" ADD CONSTRAINT "audit_entry_sequence_check" CHECK (sequence > 0);
ALTER TABLE "backplane"."binding_version" ADD CONSTRAINT "binding_version_version_check" CHECK (version > 0);
ALTER TABLE "backplane"."config_revision" ADD CONSTRAINT "config_revision_revision_check" CHECK (revision > 0);
ALTER TABLE "backplane"."console_login_attempt" ADD CONSTRAINT "console_login_attempt_attempts_check" CHECK (attempts > 0);
ALTER TABLE "backplane"."console_login_state" ADD CONSTRAINT "console_login_state_failures_check" CHECK (failures >= 0);
ALTER TABLE "backplane"."console_login_state" ADD CONSTRAINT "console_login_state_singleton_check" CHECK (singleton);
ALTER TABLE "backplane"."console_session" ADD CONSTRAINT "console_session_token_hash_key" UNIQUE ("token_hash");
ALTER TABLE "backplane"."direct_run" ADD CONSTRAINT "direct_run_attempt_check" CHECK (attempt > 0);
ALTER TABLE "backplane"."installation" ADD CONSTRAINT "installation_singleton_check" CHECK (singleton);
ALTER TABLE "backplane"."installation" ADD CONSTRAINT "installation_singleton_key" UNIQUE ("singleton");
ALTER TABLE "backplane"."rule_version" ADD CONSTRAINT "rule_version_version_check" CHECK (version > 0);
CREATE INDEX "app_audit_action_idx" ON "backplane"."app_audit" ("action", "time");
CREATE INDEX "app_audit_actor_idx" ON "backplane"."app_audit" ("actor", "time");
CREATE INDEX "app_audit_attributes_idx" ON "backplane"."app_audit" USING gin ("attributes" jsonb_path_ops);
CREATE INDEX "app_audit_service_idx" ON "backplane"."app_audit" ("service", "time");
CREATE INDEX "app_audit_subject_idx" ON "backplane"."app_audit" ("subject", "time");
CREATE INDEX "app_audit_time_idx" ON "backplane"."app_audit" ("time", "id");
CREATE INDEX "app_error_attributes_idx" ON "backplane"."app_error" USING gin ("attributes" jsonb_path_ops);
CREATE INDEX "app_error_group_idx" ON "backplane"."app_error" ("fingerprint", "service", "time" DESC);
CREATE INDEX "app_error_runtime_idx" ON "backplane"."app_error" ("runtime_id") WHERE (runtime_id <> ''::text);
CREATE INDEX "app_error_service_idx" ON "backplane"."app_error" ("service", "time" DESC);
CREATE INDEX "app_error_time_idx" ON "backplane"."app_error" ("time" DESC, "id" DESC);
CREATE INDEX "app_error_trace_idx" ON "backplane"."app_error" ("trace_id") WHERE (trace_id <> ''::text);
CREATE INDEX "app_error_type_idx" ON "backplane"."app_error" ("type", "time" DESC);
CREATE INDEX "app_error_group_last_seen_idx" ON "backplane"."app_error_group" ("last_seen");
CREATE INDEX "app_error_group_service_idx" ON "backplane"."app_error_group" ("service", "last_seen");
CREATE INDEX "audit_entry_created_at_idx" ON "backplane"."audit_entry" ("created_at");
CREATE INDEX "audit_entry_detail_idx" ON "backplane"."audit_entry" USING gin ("detail" jsonb_path_ops);
CREATE INDEX "audit_entry_operation_id_idx" ON "backplane"."audit_entry" ("operation_id");
CREATE INDEX "audit_entry_service_idx" ON "backplane"."audit_entry" ("service", "sequence");
CREATE INDEX "audit_outbox_pending_idx" ON "backplane"."audit_outbox" ("available_at", "leased_until") WHERE (published_at IS NULL);
CREATE INDEX "console_login_attempt_expires_at_idx" ON "backplane"."console_login_attempt" ("expires_at");
CREATE INDEX "console_session_expires_at_idx" ON "backplane"."console_session" ("expires_at");
CREATE INDEX "direct_run_source_idx" ON "backplane"."direct_run" ("kind", "source", "test", "started_at" DESC, "id" DESC);
CREATE INDEX "direct_run_started_at_idx" ON "backplane"."direct_run" ("started_at");
ALTER TABLE "backplane"."app_error" ADD CONSTRAINT "app_error_fingerprint_service_fkey" FOREIGN KEY ("fingerprint", "service") REFERENCES "backplane"."app_error_group" ("fingerprint", "service") ON DELETE CASCADE;
ALTER TABLE "backplane"."audit_outbox" ADD CONSTRAINT "audit_outbox_sequence_fkey" FOREIGN KEY ("sequence") REFERENCES "backplane"."audit_entry" ("sequence");
ALTER TABLE "backplane"."binding_current" ADD CONSTRAINT "binding_current_hook_version_fkey" FOREIGN KEY ("hook", "version") REFERENCES "backplane"."binding_version" ("hook", "version");
ALTER TABLE "backplane"."binding_version" ADD CONSTRAINT "binding_version_hook_rollback_of_fkey" FOREIGN KEY ("hook", "rollback_of") REFERENCES "backplane"."binding_version" ("hook", "version");
ALTER TABLE "backplane"."config_current" ADD CONSTRAINT "config_current_service_revision_fkey" FOREIGN KEY ("service", "revision") REFERENCES "backplane"."config_revision" ("service", "revision");
ALTER TABLE "backplane"."config_revision" ADD CONSTRAINT "config_revision_service_rollback_of_fkey" FOREIGN KEY ("service", "rollback_of") REFERENCES "backplane"."config_revision" ("service", "revision");
ALTER TABLE "backplane"."direct_run" ADD CONSTRAINT "direct_run_program_hash_fkey" FOREIGN KEY ("program_hash") REFERENCES "backplane"."direct_program" ("hash");
ALTER TABLE "backplane"."direct_run_step" ADD CONSTRAINT "direct_run_step_run_id_fkey" FOREIGN KEY ("run_id") REFERENCES "backplane"."direct_run" ("id") ON DELETE CASCADE;
ALTER TABLE "backplane"."rule_current" ADD CONSTRAINT "rule_current_rule_id_fkey" FOREIGN KEY ("rule_id") REFERENCES "backplane"."rule" ("id");
ALTER TABLE "backplane"."rule_current" ADD CONSTRAINT "rule_current_rule_id_version_fkey" FOREIGN KEY ("rule_id", "version") REFERENCES "backplane"."rule_version" ("rule_id", "version");
ALTER TABLE "backplane"."rule_version" ADD CONSTRAINT "rule_version_rule_id_fkey" FOREIGN KEY ("rule_id") REFERENCES "backplane"."rule" ("id");
ALTER TABLE "backplane"."rule_version" ADD CONSTRAINT "rule_version_rule_id_rollback_of_fkey" FOREIGN KEY ("rule_id", "rollback_of") REFERENCES "backplane"."rule_version" ("rule_id", "version");
CREATE VIEW "backplane"."audit_feed" AS SELECT 'platform'::text AS source,
    e.id,
    e.created_at AS "time",
    e.created_at AS received_at,
    e.service,
    e.actor,
    e.action,
    e.subject,
    e.outcome,
    ''::text AS message,
    e.operation_id::text AS operation,
    e.sequence,
    e.detail AS attributes,
    '{}'::jsonb AS resource,
    ''::text AS severity,
    ''::text AS trace_id,
    ''::text AS span_id
   FROM backplane.audit_entry e
UNION ALL
 SELECT 'application'::text AS source,
    a.id,
    a."time",
    a.received_at,
    a.service,
    a.actor,
    a.action,
    a.subject,
    a.outcome,
    a.body AS message,
    ''::text AS operation,
    0::bigint AS sequence,
    a.attributes,
    a.resource,
    a.severity,
    a.trace_id,
    a.span_id
   FROM backplane.app_audit a;

-- sqld:down
DROP VIEW "backplane"."audit_feed";
ALTER TABLE "backplane"."rule_version" DROP CONSTRAINT "rule_version_rule_id_rollback_of_fkey";
ALTER TABLE "backplane"."rule_version" DROP CONSTRAINT "rule_version_rule_id_fkey";
ALTER TABLE "backplane"."rule_current" DROP CONSTRAINT "rule_current_rule_id_version_fkey";
ALTER TABLE "backplane"."rule_current" DROP CONSTRAINT "rule_current_rule_id_fkey";
ALTER TABLE "backplane"."direct_run_step" DROP CONSTRAINT "direct_run_step_run_id_fkey";
ALTER TABLE "backplane"."direct_run" DROP CONSTRAINT "direct_run_program_hash_fkey";
ALTER TABLE "backplane"."config_revision" DROP CONSTRAINT "config_revision_service_rollback_of_fkey";
ALTER TABLE "backplane"."config_current" DROP CONSTRAINT "config_current_service_revision_fkey";
ALTER TABLE "backplane"."binding_version" DROP CONSTRAINT "binding_version_hook_rollback_of_fkey";
ALTER TABLE "backplane"."binding_current" DROP CONSTRAINT "binding_current_hook_version_fkey";
ALTER TABLE "backplane"."audit_outbox" DROP CONSTRAINT "audit_outbox_sequence_fkey";
ALTER TABLE "backplane"."app_error" DROP CONSTRAINT "app_error_fingerprint_service_fkey";
DROP INDEX "backplane"."direct_run_started_at_idx";
DROP INDEX "backplane"."direct_run_source_idx";
DROP INDEX "backplane"."console_session_expires_at_idx";
DROP INDEX "backplane"."console_login_attempt_expires_at_idx";
DROP INDEX "backplane"."audit_outbox_pending_idx";
DROP INDEX "backplane"."audit_entry_service_idx";
DROP INDEX "backplane"."audit_entry_operation_id_idx";
DROP INDEX "backplane"."audit_entry_detail_idx";
DROP INDEX "backplane"."audit_entry_created_at_idx";
DROP INDEX "backplane"."app_error_group_service_idx";
DROP INDEX "backplane"."app_error_group_last_seen_idx";
DROP INDEX "backplane"."app_error_type_idx";
DROP INDEX "backplane"."app_error_trace_idx";
DROP INDEX "backplane"."app_error_time_idx";
DROP INDEX "backplane"."app_error_service_idx";
DROP INDEX "backplane"."app_error_runtime_idx";
DROP INDEX "backplane"."app_error_group_idx";
DROP INDEX "backplane"."app_error_attributes_idx";
DROP INDEX "backplane"."app_audit_time_idx";
DROP INDEX "backplane"."app_audit_subject_idx";
DROP INDEX "backplane"."app_audit_service_idx";
DROP INDEX "backplane"."app_audit_attributes_idx";
DROP INDEX "backplane"."app_audit_actor_idx";
DROP INDEX "backplane"."app_audit_action_idx";
ALTER TABLE "backplane"."rule_version" DROP CONSTRAINT "rule_version_version_check";
ALTER TABLE "backplane"."installation" DROP CONSTRAINT "installation_singleton_key";
ALTER TABLE "backplane"."installation" DROP CONSTRAINT "installation_singleton_check";
ALTER TABLE "backplane"."direct_run" DROP CONSTRAINT "direct_run_attempt_check";
ALTER TABLE "backplane"."console_session" DROP CONSTRAINT "console_session_token_hash_key";
ALTER TABLE "backplane"."console_login_state" DROP CONSTRAINT "console_login_state_singleton_check";
ALTER TABLE "backplane"."console_login_state" DROP CONSTRAINT "console_login_state_failures_check";
ALTER TABLE "backplane"."console_login_attempt" DROP CONSTRAINT "console_login_attempt_attempts_check";
ALTER TABLE "backplane"."config_revision" DROP CONSTRAINT "config_revision_revision_check";
ALTER TABLE "backplane"."binding_version" DROP CONSTRAINT "binding_version_version_check";
ALTER TABLE "backplane"."audit_entry" DROP CONSTRAINT "audit_entry_sequence_check";
ALTER TABLE "backplane"."audit_entry" DROP CONSTRAINT "audit_entry_id_key";
ALTER TABLE "backplane"."audit_clock" DROP CONSTRAINT "audit_clock_singleton_check";
ALTER TABLE "backplane"."audit_clock" DROP CONSTRAINT "audit_clock_sequence_check";
ALTER TABLE "backplane"."audit_clock" DROP CONSTRAINT "audit_clock_retained_after_check";
ALTER TABLE "backplane"."app_error_group" DROP CONSTRAINT "app_error_group_state_check";
ALTER TABLE "backplane"."app_error" DROP CONSTRAINT "app_error_key_key";
ALTER TABLE "backplane"."app_audit" DROP CONSTRAINT "app_audit_key_key";
DROP TABLE "backplane"."rule_version";
DROP TABLE "backplane"."rule_current";
DROP TABLE "backplane"."rule";
DROP TABLE "backplane"."installation";
DROP TABLE "backplane"."direct_run_step";
DROP TABLE "backplane"."direct_run";
DROP TABLE "backplane"."direct_program";
DROP TABLE "backplane"."console_session";
DROP TABLE "backplane"."console_login_state";
DROP TABLE "backplane"."console_login_attempt";
DROP TABLE "backplane"."config_revision";
DROP TABLE "backplane"."config_current";
DROP TABLE "backplane"."binding_version";
DROP TABLE "backplane"."binding_current";
DROP TABLE "backplane"."audit_outbox";
DROP TABLE "backplane"."audit_entry";
DROP TABLE "backplane"."audit_clock";
DROP TABLE "backplane"."app_error_group";
DROP TABLE "backplane"."app_error";
DROP TABLE "backplane"."app_audit";
