-- sqld:up
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
ALTER TABLE "backplane"."console_login_attempt" ADD CONSTRAINT "console_login_attempt_attempts_check" CHECK (attempts > 0);
ALTER TABLE "backplane"."console_login_state" ADD CONSTRAINT "console_login_state_failures_check" CHECK (failures >= 0);
ALTER TABLE "backplane"."console_login_state" ADD CONSTRAINT "console_login_state_singleton_check" CHECK (singleton);
CREATE INDEX "console_login_attempt_expires_at_idx" ON "backplane"."console_login_attempt" ("expires_at");

-- sqld:down
DROP INDEX "backplane"."console_login_attempt_expires_at_idx";
ALTER TABLE "backplane"."console_login_state" DROP CONSTRAINT "console_login_state_singleton_check";
ALTER TABLE "backplane"."console_login_state" DROP CONSTRAINT "console_login_state_failures_check";
ALTER TABLE "backplane"."console_login_attempt" DROP CONSTRAINT "console_login_attempt_attempts_check";
DROP TABLE "backplane"."console_login_state";
DROP TABLE "backplane"."console_login_attempt";
