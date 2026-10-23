import { useEffect, useId, useMemo, useRef, useState, type CSSProperties, type ReactNode } from 'react';
import { create, toJsonString } from '@bufbuild/protobuf';
import { useTranslation } from 'react-i18next';
import {
  Engine, BakedSchema, SchemaSchema, Schema_FieldSchema, fieldIsActive,
  listItemDef, masked, resultBlocking, structToNative, toNative,
  type Baked, type Native, type NativeStruct, type Schema, type Schema_Field,
  type ValidationError,
} from '@gopherex/schemapb';
import { Button, Input, Textarea, Checkbox, SelectControl, Field, FieldLabel, FieldDescription, FieldError, Badge } from '@backplane-labs/backplane-ui';
import { decodeValue, emptyValue, encodeValue, evaluateForm, fieldSchema, parseScalar, pathKey, scaffoldForm, scaffoldValue, scalarText, setValue, valueAt, type FieldPath } from './model.js';
import { schemaFormsEnglish } from './locales.js';

const stack: CSSProperties = { display: 'grid', gap: 16 };
const row: CSSProperties = { display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' };
function useText() {
  const { t } = useTranslation('backplane.schemaForms');
  return (key: keyof typeof schemaFormsEnglish, args?: Record<string, string | number>) => t(key, { defaultValue: schemaFormsEnglish[key], ...args });
}

export interface SchemaFormProps {
  schema: Schema;
  /** Native schemapb values; never pass a masked display projection here. */
  initialValues?: NativeStruct;
  /** Change to load another record/revision and discard the previous draft. */
  resetKey?: string | number;
  readOnly?: boolean;
  disabled?: boolean;
  label: string;
  submitLabel?: string;
  onSubmit: (baked: Baked, signal: AbortSignal) => void | Promise<void>;
  onDirtyChange?: (dirty: boolean) => void;
}

export function SchemaForm(props: SchemaFormProps) {
  const t = useText();
  const compiled = useMemo(() => {
    try { return { engine: Engine.compile(props.schema), key: toJsonString(SchemaSchema, props.schema) }; }
    catch { return undefined; }
  }, [props.schema]);
  if (!compiled) return <p role="alert">{t('schemaError')}</p>;
  return <FormBody key={`${compiled.key}:${props.resetKey ?? ''}`} {...props} engine={compiled.engine} />;
}

function FormBody({ engine, initialValues = {}, onSubmit, onDirtyChange, ...props }: SchemaFormProps & { engine: Engine }) {
  const t = useText();
  const [baseline, setBaseline] = useState(() => evaluateForm(engine, scaffoldForm(engine.schema, initialValues)).resolved);
  const [values, setValues] = useState(baseline);
  const [invalidEditors, setInvalidEditors] = useState(new Set<string>());
  const [resetEpoch, setResetEpoch] = useState(0);
  const [saving, setSaving] = useState(false);
  const [submitFailed, setSubmitFailed] = useState(false);
  // Messages appear for a field once it is edited, and for every field once
  // a submit was attempted: an untouched form is not an error.
  const [touched, setTouched] = useState(() => new Set<string>());
  const [attempted, setAttempted] = useState(false);
  const active = useRef<AbortController | null>(null);
  const outcome = useMemo(() => evaluateForm(engine, values), [engine, values]);
  const dirty = encodeValue(values) !== encodeValue(baseline) || invalidEditors.size > 0;
  useEffect(() => { onDirtyChange?.(dirty); }, [dirty, onDirtyChange]);
  useEffect(() => () => active.current?.abort(), []);
  useEffect(() => {
    if (!dirty) return;
    const listener = (event: BeforeUnloadEvent) => { event.preventDefault(); event.returnValue = ''; };
    window.addEventListener('beforeunload', listener);
    return () => window.removeEventListener('beforeunload', listener);
  }, [dirty]);
  const disabled = props.disabled || props.readOnly || saving;
  const context: Context = {
    engine, values: outcome.resolved, errors: outcome.result.errors, disabled: !!disabled,
    shown: (path) => attempted || touched.has(pathKey(path)),
    update: (path, value) => {
      setValues((old) => setValue(old, path, value)); setSubmitFailed(false);
      setTouched((old) => old.has(pathKey(path)) ? old : new Set(old).add(pathKey(path)));
    },
    invalid: (path, invalid) => setInvalidEditors((old) => {
      const next = new Set(old); if (invalid) next.add(path); else next.delete(path); return next;
    }),
  };
  return <form aria-label={props.label} aria-busy={saving} style={stack} noValidate onSubmit={async (event) => {
    event.preventDefault();
    setAttempted(true);
    if (disabled || active.current || invalidEditors.size || !outcome.baked || resultBlocking(outcome.result)) return;
    const controller = new AbortController(); active.current = controller; setSaving(true); setSubmitFailed(false);
    try {
      await onSubmit(outcome.baked, controller.signal);
      if (!controller.signal.aborted) { setBaseline(outcome.resolved); setValues(outcome.resolved); }
    } catch { if (!controller.signal.aborted) setSubmitFailed(true); }
    finally { if (!controller.signal.aborted) { active.current = null; setSaving(false); } }
  }}>
    <div key={resetEpoch} style={stack}>
      {engine.schema.fields.map((field) => <FormField key={field.name} field={field} path={[field.name]} context={context} />)}
    </div>
    {attempted && !!outcome.result.errors.length && <section aria-label={t('errors')}>
      <ul>{outcome.result.errors.map((error, index) => <li key={index}>
        <strong>{error.path || props.label}</strong>: {error.message}
      </li>)}</ul>
    </section>}
    {submitFailed && <p role="alert">{t('submitError')}</p>}
    {!props.readOnly && <div style={row}>
      <Button type="submit" disabled={disabled || invalidEditors.size > 0}>{saving ? t('saving') : props.submitLabel ?? t('save')}</Button>
      <Button type="button" variant="outline" disabled={disabled || !dirty} onClick={() => {
        setValues(baseline); setInvalidEditors(new Set()); setResetEpoch((old) => old + 1); setSubmitFailed(false);
        setTouched(new Set()); setAttempted(false);
      }}>{t('reset')}</Button>
      {dirty && <span role="status">{t('dirty')}</span>}
    </div>}
  </form>;
}

interface Context {
  engine: Engine;
  values: NativeStruct;
  errors: ValidationError[];
  disabled: boolean;
  /** Whether the field's validation messages are shown yet. */
  shown: (path: FieldPath) => boolean;
  update: (path: FieldPath, value: Native | undefined) => void;
  invalid: (path: string, invalid: boolean) => void;
}

function FormField({ field, path, context, depth = 0 }: { field: Schema_Field; path: FieldPath; context: Context; depth?: number }) {
  const t = useText();
  const id = useId();
  const value = valueAt(context.values, path);
  const title = field.title || field.name || String(path[path.length - 1]);
  const disabled = context.disabled || field.immutable || field.kind.case === 'computed';
  const errors = context.shown(path) ? context.errors.filter((error) => pathKey(error.pathSegments.map((p) => p.segment.case === 'index' ? Number(p.segment.value) : p.segment.value ?? '')) === pathKey(path)) : [];
  if (!fieldIsActive(context.engine, field, context.values, '', undefined)) return null;
  const set = (next: Native | undefined) => context.update(path, next);
  const aria = { id, 'aria-describedby': `${id}-help ${id}-errors`, 'aria-invalid': errors.length > 0, disabled };
  // Plain values are edited in place even while absent: typing sets the
  // value, clearing an optional one unsets it. Structures keep the explicit
  // "set value" step, as an empty object or list is itself a value.
  const inline = !field.secret && value === undefined && inlineKinds.has(field.kind.case ?? '');
  let control: ReactNode;
  if (inline && field.kind.case === 'bool') {
    control = <div style={row}><Checkbox {...aria} checked={false} onCheckedChange={(checked) => set(checked === true)} /><span>{t('absent')}</span></div>;
  } else if (inline && field.kind.case === 'choice') {
    control = <ChoiceControl {...aria} field={field} value={undefined} onChange={set} context={context} />;
  } else if (inline) {
    control = <ScalarControl {...aria} field={field} value={undefined} onChange={set} optional={!field.required} />;
  } else if (value === undefined || value === null) {
    control = <div style={row}><span>{t(value === null ? 'null' : 'absent')}</span>
      {!disabled && <Button type="button" variant="outline" onClick={() => set(scaffoldValue(context.engine.schema, field))}>{t('add')}</Button>}
    </div>;
  } else if (field.secret) {
    control = <SecretControl {...aria} title={title} value={value} field={field} onChange={set} path={path} context={context} />;
  } else if (depth >= 12) {
    control = <><p>{t('depth')}</p><StructuredControl {...aria} value={value} path={path} context={context} /></>;
  } else if (['object', 'ref', 'oneOf'].includes(field.kind.case ?? '')) {
    const nested = fieldSchema(context.engine.schema, field, value);
    const union = field.kind.case === 'oneOf' ? field.kind.value : undefined;
    control = <fieldset style={{ ...stack, padding: 16, border: '1px solid var(--border)', minWidth: 0 }} disabled={disabled}>
      <legend>{title}</legend>
      {union && <SelectControl aria-label={t('variant', { name: title })} value={String((value as NativeStruct)[union.discriminator] ?? '')} disabled={disabled}
        onValueChange={(next) => set({ [union.discriminator]: next })} options={[{ value: '', label: t('choose'), disabled: true }, ...Object.keys(union.variants).map((name) => ({ value: name, label: name }))]} />}
      {nested ? nested.fields.filter((f) => f.name !== union?.discriminator).map((child) => <FormField key={child.name} field={child} path={[...path, child.name]} context={{ ...context, disabled }} depth={depth + 1} />)
        : !union && <StructuredControl {...aria} value={value} path={path} context={context} />}
    </fieldset>;
  } else if (field.kind.case === 'list' && Array.isArray(value)) {
    control = <ListControl list={field.kind.value} value={value} title={title} id={id} path={path} context={context} disabled={disabled} depth={depth} />;
  } else if (field.kind.case === 'map' && typeof value === 'object' && !Array.isArray(value)) {
    const map = field.kind.value;
    control = <div style={stack}>{Object.keys(value).map((key) => {
      const child = map.valueField ?? (map.valueSchema ? create(Schema_FieldSchema, { kind: { case: 'object', value: { schema: map.valueSchema } } }) : undefined);
      return <div key={key} style={stack}>
        {child ? <FormField field={{ ...child, title: key }} path={[...path, key]} context={{ ...context, disabled }} depth={depth + 1} />
          : <StructuredControl id={`${id}-${encodeURIComponent(key)}`} label={key} value={(value as NativeStruct)[key]} path={[...path, key]} context={context} disabled={disabled} />}
        {!disabled && <Button type="button" variant="outline" onClick={() => context.update([...path, key], undefined)}>{t('remove', { name: key })}</Button>}
      </div>;
    })}{!disabled && <MapEntry title={title} value={value as NativeStruct} onAdd={(key) => context.update([...path, key], map.valueField ? emptyValue(map.valueField) : {})} />}</div>;
  } else if (field.kind.case === 'bool') {
    // The field column stretches its children; a checkbox keeps its size.
    control = <div style={row}><Checkbox {...aria} checked={value === true} onCheckedChange={(checked) => set(checked === true)} /></div>;
  } else if (field.kind.case === 'choice') {
    control = <div style={stack}><ChoiceControl {...aria} field={field} value={value} onChange={set} context={context} />
      {field.kind.value.open && <StructuredControl id={`${id}-custom`} value={value} path={path} context={context} disabled={disabled} />}</div>;
  } else if (field.kind.case === 'json' || !field.kind.case || typeof value === 'object' && field.kind.case === 'computed') {
    control = <StructuredControl {...aria} value={value} path={path} context={context} />;
  } else {
    control = <ScalarControl {...aria} field={field} value={value} onChange={set} optional={!field.required} />;
  }
  // Clearing an inline value already unsets it; only structures need the
  // explicit action. Both presence actions sit beside the label.
  const unsettable = value !== undefined && !field.required && (value === null || !inlineKinds.has(field.kind.case ?? '') || !!field.secret);
  return <Field data-invalid={errors.length > 0}>
    <div style={row}><FieldLabel htmlFor={id}>{title}{field.required ? ' *' : ''}{field.unit ? ` (${field.unit})` : ''}</FieldLabel>
      {field.deprecated && <Badge variant="outline">{t('deprecated')}</Badge>}
      {field.immutable && <Badge variant="outline">{t('immutable')}</Badge>}
      {field.kind.case === 'computed' && <Badge variant="outline">{t('computed')}</Badge>}
      {!disabled && (unsettable || value !== null && field.nullable) && <span style={{ ...row, marginInlineStart: 'auto' }}>
        {unsettable && <Button type="button" variant="ghost" size="sm" onClick={() => set(undefined)}>{t('unset')}</Button>}
        {value !== null && field.nullable && <Button type="button" variant="ghost" size="sm" onClick={() => set(null)}>{t('nullValue')}</Button>}
      </span>}
    </div>
    {control}
    <FieldDescription id={`${id}-help`}>{field.description}{field.secret ? ` ${t('secret')}` : ''}</FieldDescription>
    <FieldError id={`${id}-errors`} errors={errors} />
  </Field>;
}

function ListControl({ list, value, title, id, path, context, disabled, depth }: {
  list: import('@gopherex/schemapb').Schema_Field_List; value: Native[]; title: string; id: string;
  path: FieldPath; context: Context; disabled: boolean; depth: number;
}) {
  const t = useText();
  const keys = useRef<string[]>([]); const serial = useRef(0);
  while (keys.current.length < value.length) keys.current.push(`${id}-${serial.current++}`);
  keys.current.length = value.length;
  return <div style={stack}>{value.map((entry, index) => {
    const item = listItemDef(list, index);
    return <div key={keys.current[index]} style={stack}>
      {item ? <FormField field={{ ...item, title: t('item', { index: index + 1 }) }} path={[...path, index]} context={{ ...context, disabled }} depth={depth + 1} />
        : <StructuredControl id={`${id}-${index}`} value={entry} path={[...path, index]} context={context} disabled={disabled} />}
      {!disabled && <Button type="button" variant="outline" aria-label={t('remove', { name: `${title} ${index + 1}` })} onClick={() => {
        keys.current.splice(index, 1);
        context.update(path, value.filter((_, i) => i !== index));
      }}>{t('remove', { name: t('item', { index: index + 1 }) })}</Button>}
    </div>;
  })}{!disabled && <Button type="button" variant="outline" onClick={() => {
    const item = listItemDef(list, value.length); context.update(path, [...value, item ? emptyValue(item) : null]);
  }}>{t('addItem')}</Button>}</div>;
}

/** The kinds edited in place while absent. */
const inlineKinds = new Set(['string', 'int32', 'int64', 'uint32', 'uint64', 'double', 'float', 'duration', 'timestamp', 'bytes', 'bool', 'choice']);

type ControlProps = { id: string; disabled?: boolean; value: Native; 'aria-describedby'?: string; 'aria-invalid'?: boolean };
function ScalarControl({ field, value, onChange, optional, ...props }: Omit<ControlProps, 'value'> & {
  field: Schema_Field; value: Native | undefined; onChange: (value: Native | undefined) => void; optional?: boolean;
}) {
  const t = useText();
  // Keep lexical edits such as '-', '1.' and leading zeros until blur.
  const [draft, setDraft] = useState<string | null>(null);
  return <Input {...props} value={draft ?? scalarText(value)} placeholder={value === undefined ? t('absent') : undefined}
    onFocus={() => setDraft(scalarText(value))} onBlur={() => setDraft(null)}
    inputMode={['int32', 'int64', 'uint32', 'uint64', 'double', 'float'].includes(field.kind.case ?? '') ? 'decimal' : undefined}
    onChange={(event) => {
      setDraft(event.target.value);
      onChange(optional && event.target.value === '' ? undefined : parseScalar(field, event.target.value));
    }} />;
}

function ChoiceControl({ field, value, onChange, context, ...props }: Omit<ControlProps, 'value'> & {
  field: Schema_Field; value: Native | undefined; onChange: (value: Native | undefined) => void; context: Context;
}) {
  const t = useText();
  if (field.kind.case !== 'choice') return null;
  const choice = field.kind.value;
  const dynamic = choice.optionsExpr ? context.engine.eval(choice.optionsExpr, { root: context.values }) : undefined;
  const options = dynamic?.ok && Array.isArray(dynamic.value) ? dynamic.value : choice.options.map((option) => toNative(option.value));
  const selected = value === undefined ? -1 : options.findIndex((option) => encodeValue(option) === encodeValue(value));
  // An optional choice can return to "not set"; a value outside the options
  // (an open choice's custom entry) is shown as the placeholder.
  const placeholder = value === undefined ? t('absent') : selected < 0 ? scalarText(value) || t('choose') : t('choose');
  return <SelectControl {...props} value={selected < 0 ? '' : String(selected)}
    onValueChange={(next) => onChange(next === '' ? undefined : options[Number(next)])}
    options={[{ value: '', label: placeholder, disabled: !!field.required || value === undefined },
      ...options.map((option, index) => ({ value: String(index), label: !choice.optionsExpr && choice.options[index]?.label || scalarText(option) }))]} />;
}

function SecretControl({ field, value, title, onChange, path, context, ...props }: ControlProps & { field: Schema_Field; title: string; onChange: (value: Native) => void; path: FieldPath; context: Context }) {
  const t = useText();
  const [visible, setVisible] = useState(false);
  if (props.disabled) return <Input {...props} type="text" value="***" readOnly />;
  const structured = typeof value === 'object' || typeof value === 'boolean';
  return <div style={row}>{structured
    ? visible ? <StructuredControl {...props} value={value} path={path} context={context} label={title} /> : <Input {...props} value="***" readOnly />
    : <Input {...props} autoComplete="new-password" type={visible ? 'text' : 'password'} value={scalarText(value)} onChange={(event) => onChange(parseScalar(field, event.target.value))} />}
    <Button type="button" variant="outline" disabled={props.disabled} aria-pressed={visible} onClick={() => setVisible(!visible)}>{t(visible ? 'hide' : 'show', { name: title })}</Button>
  </div>;
}

function StructuredControl({ value, path, context, label, ...props }: ControlProps & { path: FieldPath; context: Context; label?: string }) {
  const t = useText();
  const [draft, setDraft] = useState<string | null>(null);
  const [invalid, setInvalid] = useState(false);
  const reportInvalid = useRef(context.invalid); reportInvalid.current = context.invalid;
  const key = pathKey(path);
  useEffect(() => () => reportInvalid.current(key, false), [key]);
  return <div style={stack}>
    <Textarea {...props} aria-label={label ?? t('typedJSON')} aria-invalid={invalid || props['aria-invalid']} rows={5} spellCheck={false} value={draft ?? encodeValue(value)}
      onBlur={() => { if (!invalid) setDraft(null); }} onChange={(event) => {
        setDraft(event.target.value);
        try { const next = decodeValue(event.target.value); context.update(path, next); setInvalid(false); context.invalid(key, false); }
        catch { setInvalid(true); context.invalid(key, true); }
      }} />
    <FieldDescription>{t('typedJSONHelp')}</FieldDescription>
    {invalid && <FieldError>{t('invalidJSON')}</FieldError>}
  </div>;
}

function MapEntry({ title, value, onAdd }: { title: string; value: NativeStruct; onAdd: (key: string) => void }) {
  const t = useText(); const [key, setKey] = useState(''); const exists = Object.hasOwn(value, key);
  return <div style={row}><Input aria-label={t('key', { name: title })} value={key} aria-invalid={exists} onChange={(event) => setKey(event.target.value)} />
    <Button type="button" variant="outline" disabled={exists || !key} onClick={() => { onAdd(key); setKey(''); }}>{t('addEntry')}</Button>
    {exists && <FieldError>{t('duplicateKey')}</FieldError>}
  </div>;
}

/** Read-only projection; deliberately cannot be used as an editable form. */
export function SchemaValuesPreview({ schema, values, label }: { schema: Schema; values: Baked['values']; label?: string }) {
  const t = useText();
  const safe = masked(create(BakedSchema, { schema, values }));
  return <pre aria-label={label ?? t('preview')} style={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere', fontFamily: 'var(--font-mono)' }}>{encodeValue(structToNative(safe))}</pre>;
}
