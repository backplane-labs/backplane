-- sqld:up
ALTER TABLE "backplane"."direct_run" ADD COLUMN "expire_at" timestamptz;
CREATE INDEX "direct_run_expire_idx" ON "backplane"."direct_run" ("expire_at") WHERE (expire_at IS NOT NULL);
DROP INDEX "backplane"."direct_run_started_at_idx";
CREATE INDEX "direct_run_started_at_idx" ON "backplane"."direct_run" ("started_at") WHERE (expire_at IS NULL);

-- sqld:down
DROP INDEX "backplane"."direct_run_started_at_idx";
CREATE INDEX "direct_run_started_at_idx" ON "backplane"."direct_run" ("started_at");
DROP INDEX "backplane"."direct_run_expire_idx";
ALTER TABLE "backplane"."direct_run" DROP COLUMN "expire_at";
