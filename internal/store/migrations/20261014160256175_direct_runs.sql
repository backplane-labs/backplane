-- sqld:up
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
  "program" json NOT NULL,
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
ALTER TABLE "backplane"."direct_run" ADD CONSTRAINT "direct_run_attempt_check" CHECK (attempt > 0);
CREATE INDEX "direct_run_source_idx" ON "backplane"."direct_run" ("kind", "source", "test", "started_at" DESC, "id" DESC);
CREATE INDEX "direct_run_started_at_idx" ON "backplane"."direct_run" ("started_at");
ALTER TABLE "backplane"."direct_run_step" ADD CONSTRAINT "direct_run_step_run_id_fkey" FOREIGN KEY ("run_id") REFERENCES "backplane"."direct_run" ("id") ON DELETE CASCADE;

-- sqld:down
ALTER TABLE "backplane"."direct_run_step" DROP CONSTRAINT "direct_run_step_run_id_fkey";
DROP INDEX "backplane"."direct_run_started_at_idx";
DROP INDEX "backplane"."direct_run_source_idx";
ALTER TABLE "backplane"."direct_run" DROP CONSTRAINT "direct_run_attempt_check";
DROP TABLE "backplane"."direct_run_step";
DROP TABLE "backplane"."direct_run";
