-- sqld:up
ALTER TABLE "backplane"."direct_run" ADD COLUMN "deadline_at" timestamptz NOT NULL DEFAULT clock_timestamp();
CREATE INDEX "direct_run_deadline_idx" ON "backplane"."direct_run" ("deadline_at") WHERE (status = 'running'::text);

-- sqld:down
DROP INDEX "backplane"."direct_run_deadline_idx";
ALTER TABLE "backplane"."direct_run" DROP COLUMN "deadline_at";
