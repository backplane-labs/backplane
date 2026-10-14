import { create, type JsonValue } from '@bufbuild/protobuf';
import { timestampFromMs, ValueSchema } from '@bufbuild/protobuf/wkt';
import { AuditConditionSchema, AuditField, AuditFilterSchema, AuditOperator, type AuditCondition, type AuditFilter, type AuditRecord } from '@backplane-labs/backplane-api';
import { allOps, type FeedChip, type FeedOp } from './feed.js';

/** A fixed field of an audit record, as the feed names it. */
export type AuditFieldName = 'source' | 'service' | 'action' | 'actor' | 'subject' | 'outcome' | 'operation' | 'severity' | 'trace_id';
/** An audit chip targets `field:<name>` or `attr:<key>`. */
export type AuditChip = FeedChip;

export const auditFields: readonly AuditFieldName[] = ['source', 'service', 'action', 'actor', 'subject', 'outcome', 'operation', 'severity', 'trace_id'];
const fieldEnum: Record<AuditFieldName, AuditField> = {
  source: AuditField.SOURCE, service: AuditField.SERVICE, action: AuditField.ACTION, actor: AuditField.ACTOR, subject: AuditField.SUBJECT,
  outcome: AuditField.OUTCOME, operation: AuditField.OPERATION, severity: AuditField.SEVERITY, trace_id: AuditField.TRACE_ID,
};
const opEnum: Record<FeedOp, AuditOperator> = {
  is: AuditOperator.IS, is_not: AuditOperator.IS_NOT, contains: AuditOperator.CONTAINS, not_contains: AuditOperator.NOT_CONTAINS,
  prefix: AuditOperator.PREFIX, exists: AuditOperator.EXISTS, not_exists: AuditOperator.NOT_EXISTS, gt: AuditOperator.GT,
  gte: AuditOperator.GTE, lt: AuditOperator.LT, lte: AuditOperator.LTE,
};

export const fieldKey = (field: AuditFieldName) => `field:${field}`;
export const attributeKey = (attribute: string) => `attr:${attribute}`;
const fieldOf = (key: string): AuditFieldName | undefined => key.startsWith('field:') && auditFields.includes(key.slice(6) as AuditFieldName) ? key.slice(6) as AuditFieldName : undefined;

const valueOf = (value: JsonValue) => create(ValueSchema, value === null ? { kind: { case: 'nullValue', value: 0 } }
  : typeof value === 'boolean' ? { kind: { case: 'boolValue', value } } : typeof value === 'number' ? { kind: { case: 'numberValue', value } }
  : { kind: { case: 'stringValue', value: typeof value === 'string' ? value : JSON.stringify(value) } });

/** The condition a chip key targets. */
export function conditionOf(key: string): AuditCondition {
  const field = fieldOf(key);
  return create(AuditConditionSchema, { target: field ? { case: 'field', value: fieldEnum[field] } : { case: 'attribute', value: key.slice(5) } });
}

/** The chip key of a facet's target. */
export function keyOf(condition?: AuditCondition): string {
  const target = condition?.target;
  return target?.case === 'attribute' ? attributeKey(target.value) : fieldKey(auditFields[(target?.value as number ?? 1) - 1]!);
}

/** The request filter of the page's chips, text and resolved range. */
export function filterOf(chips: readonly AuditChip[], text: string, range?: { from: number; to: number }): AuditFilter {
  return create(AuditFilterSchema, {
    text, start: range ? timestampFromMs(range.from) : undefined, end: range ? timestampFromMs(range.to) : undefined,
    conditions: chips.map((chip) => ({ ...conditionOf(chip.key), op: opEnum[chip.op], values: chip.values.map(valueOf) })),
  });
}

/** Chips as a URL parameter and back; malformed input is dropped. */
export function encodeChips(chips: readonly FeedChip[]): string { return chips.length ? JSON.stringify(chips) : ''; }
export function decodeChips(raw: string | null, validKey: (key: string) => boolean = (key) => !!fieldOf(key) || (key.startsWith('attr:') && key.length > 5)): FeedChip[] {
  if (!raw) return [];
  try {
    const parsed: unknown = JSON.parse(raw);
    if (!Array.isArray(parsed)) return [];
    return parsed.filter((chip): chip is FeedChip => !!chip && typeof chip === 'object' && typeof (chip as FeedChip).key === 'string' && validKey((chip as FeedChip).key)
      && allOps.includes((chip as FeedChip).op) && Array.isArray((chip as FeedChip).values));
  } catch { return []; }
}

/** The latest first page merged over the shown records: new ones on top, the rest kept, newest first by time then id. */
export function mergeRecords(shown: readonly AuditRecord[], latest: readonly AuditRecord[]): AuditRecord[] {
  const byId = new Map(shown.map((record) => [record.id, record]));
  for (const record of latest) byId.set(record.id, record);
  const time = (record: AuditRecord) => record.time ? Number(record.time.seconds) * 1e3 + record.time.nanos / 1e6 : 0;
  return [...byId.values()].sort((a, b) => time(b) - time(a) || (a.id < b.id ? 1 : a.id > b.id ? -1 : 0));
}

/** An actor as people read it: console sessions and rules shortened. */
export function humanActor(actor: string): string {
  const [kind, id] = actor.includes(':') ? [actor.slice(0, actor.indexOf(':')), actor.slice(actor.indexOf(':') + 1)] : [actor, ''];
  if (kind === 'console' && id) return `operator · ${id.slice(0, 8)}`;
  if (kind === 'rule' && id) return `rule · ${id.slice(0, 8)}`;
  return actor;
}

/** Keys loggers add to every record (the SDK's xlog, OTel bridges): context, not facts. */
const logContext = new Set(['event.name', 'trace_id', 'span_id', 'service', 'instance', 'version', 'node', 'component', 'logger', 'caller']);

/** The facts worth a glance in the table's details column. */
export function detailFacts(record: AuditRecord): string[] {
  const attributes = (record.attributes ?? {}) as Record<string, JsonValue>, facts: string[] = [];
  if (record.message) facts.push(record.message);
  const show = (key: string, value: JsonValue | undefined, format = (v: JsonValue) => typeof v === 'string' ? v : JSON.stringify(v)) => {
    if (value !== undefined && value !== null && value !== '') facts.push(`${key} ${format(value)}`);
  };
  if (record.source === 1) { // platform: its detail
    show('rev', attributes.revision); show('rollback of', attributes.rollback_of); show('keys', attributes.keys, (v) => Array.isArray(v) ? v.join(', ') : String(v));
    show('workflow', attributes.workflow_id); show('signal', attributes.signal); show('reason', attributes.reason); show('note', attributes.note);
    show('code', attributes.code); show('affected', attributes.affected); if (attributes.paused !== undefined) facts.push(attributes.paused ? 'paused' : 'resumed');
    return facts;
  }
  for (const [key, value] of Object.entries(attributes)) {
    if (facts.length >= 3) break;
    if (key.startsWith('backplane.') || logContext.has(key)) continue;
    show(key, value);
  }
  return facts;
}

export const outcomeTone = (outcome: string) => outcome === 'succeeded' ? 'success' as const : outcome === 'failed' || outcome === 'rejected' ? 'danger' as const
  : outcome === 'partial' ? 'warning' as const : outcome === 'intent' ? 'info' as const : 'neutral' as const;
