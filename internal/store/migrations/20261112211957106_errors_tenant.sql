-- sqld:up
CREATE TABLE "backplane"."app_error_burst" (
  "fingerprint" bytea NOT NULL,
  "service" text NOT NULL,
  "tenant" text NOT NULL,
  "minute_start" timestamptz NOT NULL,
  "minute_count" int4 NOT NULL DEFAULT 0,
  "day_start" timestamptz NOT NULL,
  "day_count" int4 NOT NULL DEFAULT 0,
  PRIMARY KEY ("fingerprint", "service", "tenant")
);
ALTER TABLE "backplane"."app_error" ADD COLUMN "tenant" text NOT NULL DEFAULT ''::text;
ALTER TABLE "backplane"."app_error_group" DROP COLUMN "day_count";
ALTER TABLE "backplane"."app_error_group" DROP COLUMN "day_start";
ALTER TABLE "backplane"."app_error_group" DROP COLUMN "minute_count";
ALTER TABLE "backplane"."app_error_group" DROP COLUMN "minute_start";
CREATE INDEX "app_error_tenant_idx" ON "backplane"."app_error" ("tenant", "time" DESC) WHERE (tenant <> ''::text);
ALTER TABLE "backplane"."app_error_burst" ADD CONSTRAINT "app_error_burst_fingerprint_service_fkey" FOREIGN KEY ("fingerprint", "service") REFERENCES "backplane"."app_error_group" ("fingerprint", "service") ON DELETE CASCADE;

-- sqld:down
ALTER TABLE "backplane"."app_error_burst" DROP CONSTRAINT "app_error_burst_fingerprint_service_fkey";
DROP INDEX "backplane"."app_error_tenant_idx";
-- WARNING: data loss on rollback
ALTER TABLE "backplane"."app_error_group" ADD COLUMN "minute_start" timestamptz NOT NULL;
-- WARNING: data loss on rollback
ALTER TABLE "backplane"."app_error_group" ADD COLUMN "minute_count" int4 NOT NULL DEFAULT 0;
-- WARNING: data loss on rollback
ALTER TABLE "backplane"."app_error_group" ADD COLUMN "day_start" timestamptz NOT NULL;
-- WARNING: data loss on rollback
ALTER TABLE "backplane"."app_error_group" ADD COLUMN "day_count" int4 NOT NULL DEFAULT 0;
ALTER TABLE "backplane"."app_error" DROP COLUMN "tenant";
DROP TABLE "backplane"."app_error_burst";
