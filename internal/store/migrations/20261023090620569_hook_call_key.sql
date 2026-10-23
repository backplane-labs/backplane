-- sqld:up
ALTER TABLE "backplane"."direct_run" ADD COLUMN "reply" bytea;

-- sqld:down
ALTER TABLE "backplane"."direct_run" DROP COLUMN "reply";
