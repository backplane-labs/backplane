import { useEffect, useId, useLayoutEffect, useRef, useState, type ComponentProps, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from './button.js';
import { Input } from './input.js';
import { Textarea } from './textarea.js';
import { FieldError } from './field.js';
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle, DialogTrigger } from './dialog.js';
import { decimal, stepDecimal } from '../lib/decimal.js';

export function Stack({ gap = 16, direction = 'column', style, ...props }: ComponentProps<'div'> & { gap?: number; direction?: 'row' | 'column' }) {
  return <div {...props} style={{ display: 'flex', flexDirection: direction, gap, minWidth: 0, ...style }} />;
}
export function Grid({ minWidth = 240, gap = 16, style, ...props }: ComponentProps<'div'> & { minWidth?: number; gap?: number }) {
  return <div {...props} style={{ display: 'grid', gridTemplateColumns: `repeat(auto-fit,minmax(min(100%,${minWidth}px),1fr))`, gap, ...style }} />;
}
export function Text({ muted, mono, style, ...props }: ComponentProps<'span'> & { muted?: boolean; mono?: boolean }) {
  return <span {...props} style={{ color: muted ? 'var(--muted-foreground)' : undefined, fontFamily: mono ? 'var(--font-mono)' : undefined, ...style }} />;
}
export function TextLink({ style, ...props }: ComponentProps<'a'>) {
  return <a {...props} style={{ color: 'var(--link)', textDecoration: 'underline', textUnderlineOffset: 3, ...style }} />;
}
export function IconButton({ label, ...props }: Omit<ComponentProps<typeof Button>, 'aria-label'> & { label: string }) {
  return <Button size="icon" {...props} aria-label={label} />;
}

export function ClipboardButton({ value, label, disabled }: { value: string; label?: string; disabled?: boolean }) {
  const { t } = useTranslation('backplane.ui');
  const [status, setStatus] = useState<'idle' | 'copied' | 'failed'>('idle');
  useEffect(() => { setStatus('idle'); }, [value]);
  return <span className="inline-flex items-center gap-2"><Button type="button" variant="outline" disabled={disabled} onClick={async () => {
    try { await navigator.clipboard.writeText(value); setStatus('copied'); } catch { setStatus('failed'); }
  }}>{label ?? t('copy')}</Button><span role="status">{status === 'idle' ? '' : t(status === 'copied' ? 'copied' : 'copyFailed')}</span></span>;
}

export interface ConfirmActionProps {
  trigger: ReactNode; title: string; description: string; confirmLabel?: string;
  onConfirm: (signal: AbortSignal) => void | Promise<void>; disabled?: boolean;
  /** danger: the confirm button takes the destructive style (deleting, erasing). */
  tone?: 'default' | 'danger';
  /** Fields the confirmation asks for (a note, a reason), between the description and the buttons. */
  children?: ReactNode;
}
export function ConfirmAction({ trigger, title, description, confirmLabel, onConfirm, disabled, tone, children }: ConfirmActionProps) {
  const { t } = useTranslation('backplane.ui');
  const [open, setOpen] = useState(false), [pending, setPending] = useState(false), [error, setError] = useState(false);
  const active = useRef<AbortController | null>(null);
  useEffect(() => () => active.current?.abort(), []);
  return <Dialog open={open} onOpenChange={(value) => { if (!pending) { setOpen(value); setError(false); } }}>
    <DialogTrigger asChild><Button type="button" variant="outline" disabled={disabled}>{trigger}</Button></DialogTrigger>
    <DialogContent><DialogHeader><DialogTitle>{title}</DialogTitle><DialogDescription>{description}</DialogDescription></DialogHeader>
      {children}
      {error && <FieldError>{t('actionFailed')}</FieldError>}
      <DialogFooter><Button type="button" variant="outline" disabled={pending} onClick={() => setOpen(false)}>{t('cancel')}</Button>
        <Button type="button" variant={tone === 'danger' ? 'destructive' : 'default'} disabled={pending} onClick={async () => {
          if (active.current) return;
          const controller = new AbortController(); active.current = controller; setPending(true); setError(false);
          try { await onConfirm(controller.signal); if (!controller.signal.aborted) setOpen(false); }
          catch { if (!controller.signal.aborted) setError(true); }
          finally { if (!controller.signal.aborted) { active.current = null; setPending(false); } }
        }}>{pending ? t('loading') : confirmLabel ?? t('confirm')}</Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>;
}

export function SearchInput({ value, onValueChange, label, disabled, ...props }: Omit<ComponentProps<typeof Input>, 'value' | 'onChange' | 'type'> & { value: string; onValueChange: (value: string) => void; label: string }) {
  const { t } = useTranslation('backplane.ui');
  return <div className="flex items-center gap-2"><Input {...props} disabled={disabled} type="search" aria-label={label} value={value} onChange={(event) => onValueChange(event.target.value)} />
    {value && <Button type="button" size="sm" variant="ghost" disabled={disabled} aria-label={t('clearSearch')} onClick={() => onValueChange('')}>{t('clear')}</Button>}
  </div>;
}

export function SecretInput({ value, onValueChange, label, multiline = false, disabled, readOnly }: { value: string; onValueChange: (value: string) => void; label: string; multiline?: boolean; disabled?: boolean; readOnly?: boolean }) {
  const { t } = useTranslation('backplane.ui'); const [show, setShow] = useState(false);
  return <Stack gap={8}>
    {multiline && show && !readOnly
      ? <Textarea aria-label={label} disabled={disabled} value={value} onChange={(event) => onValueChange(event.target.value)} autoComplete="off" />
      : <Input aria-label={label} disabled={disabled} readOnly={readOnly || multiline} autoComplete="new-password" type={show && !readOnly ? 'text' : 'password'} value={readOnly ? '***' : multiline ? '***' : value} onChange={(event) => onValueChange(event.target.value)} />}
    {!readOnly && <Button type="button" variant="outline" disabled={disabled} aria-pressed={show} onClick={() => setShow(!show)}>{t(show ? 'hideSecret' : 'showSecret')}</Button>}
  </Stack>;
}

export function NumberInput({ label, value, onValueChange, step = '1', min, max, unit, disabled, readOnly }: {
  label: string; value: string; onValueChange: (value: string) => void; step?: string; min?: string; max?: string; unit?: string; disabled?: boolean; readOnly?: boolean;
}) {
  const { t } = useTranslation('backplane.ui'); const id = useId();
  return <div className="flex items-center gap-2"><Input aria-label={label} aria-describedby={unit ? id : undefined} inputMode="decimal" disabled={disabled} readOnly={readOnly} value={value} aria-invalid={!!value && !decimal(value)} onChange={(event) => onValueChange(event.target.value)}
    onKeyDown={(event) => { if (!disabled && !readOnly && ['ArrowUp', 'ArrowDown'].includes(event.key)) { event.preventDefault(); onValueChange(stepDecimal(value, step, event.key === 'ArrowUp' ? 1 : -1, min, max)); } }} />
    {unit && <span id={id}>{unit}</span>}
    <Button type="button" variant="outline" size="icon" disabled={disabled || readOnly} aria-label={t('decrease', { name: label })} onClick={() => onValueChange(stepDecimal(value, step, -1, min, max))}>−</Button>
    <Button type="button" variant="outline" size="icon" disabled={disabled || readOnly} aria-label={t('increase', { name: label })} onClick={() => onValueChange(stepDecimal(value, step, 1, min, max))}>+</Button>
  </div>;
}

/** ISO calendar/time text; converting it to an instant requires caller timezone. */
export function DateTimeInput({ kind = 'datetime-local', label, ...props }: Omit<ComponentProps<typeof Input>, 'type'> & { kind?: 'date' | 'time' | 'datetime-local'; label: string }) {
  return <Input {...props} type={kind} aria-label={label} />;
}

export function StateMessage({ title, children, tone = 'empty', onRetry }: { title: string; children?: ReactNode; tone?: 'empty' | 'error' | 'offline' | 'partial'; onRetry?: () => void }) {
  const { t } = useTranslation('backplane.ui');
  return <section className="rounded-md border p-4" role={tone === 'error' ? 'alert' : 'status'}>
    <strong>{title}</strong>{children && <div className="mt-2 text-muted-foreground">{children}</div>}
    {onRetry && <Button type="button" variant="outline" className="mt-3" onClick={onRetry}>{t('retry')}</Button>}
  </section>;
}

export function AutoSizeInput({ value, minCharacters = 4, maxCharacters = 80, style, ...props }: ComponentProps<typeof Input> & { minCharacters?: number; maxCharacters?: number }) {
  return <Input {...props} value={value} style={{ width: `${Math.max(minCharacters, Math.min(maxCharacters, String(value ?? '').length + 2))}ch`, maxWidth: '100%', ...style }} />;
}

export function AutoSizeTextarea({ value, onChange, maxHeight = 320, ...props }: Omit<ComponentProps<typeof Textarea>, 'ref'> & { maxHeight?: number }) {
  const ref = useRef<HTMLTextAreaElement>(null);
  const resize = () => { if (ref.current) { ref.current.style.height = 'auto'; ref.current.style.height = `${Math.min(maxHeight, ref.current.scrollHeight)}px`; } };
  useLayoutEffect(resize, [value, maxHeight]);
  return <Textarea {...props} ref={ref} value={value} onChange={(event) => { resize(); onChange?.(event); }} />;
}

/** Saves on blur. Failed mutations stay editable and are retried explicitly. */
export function AutoSaveInput({ defaultValue, label, onSave, disabled }: { defaultValue: string; label: string; onSave: (value: string, signal: AbortSignal) => Promise<void>; disabled?: boolean }) {
  const { t } = useTranslation('backplane.ui');
  const [value, setValue] = useState(defaultValue), [saved, setSaved] = useState(defaultValue), [pending, setPending] = useState(false), [error, setError] = useState(false);
  const active = useRef<AbortController | null>(null);
  useEffect(() => () => { active.current?.abort(); active.current = null; }, []);
  const save = async () => {
    if (disabled || active.current || value === saved) return;
    const controller = new AbortController(); active.current = controller; setPending(true); setError(false);
    try { await onSave(value, controller.signal); if (!controller.signal.aborted) setSaved(value); }
    catch { if (!controller.signal.aborted) setError(true); }
    finally { if (active.current === controller) { active.current = null; setPending(false); } }
  };
  return <Stack gap={8}><Input aria-label={label} value={value} disabled={disabled || pending} aria-invalid={error} onChange={(event) => setValue(event.target.value)} onBlur={() => { if (!error) void save(); }} />
    {pending && <span role="status">{t('saving')}</span>}
    {error && <div><FieldError>{t('actionFailed')}</FieldError><Button type="button" variant="outline" disabled={disabled} onClick={() => void save()}>{t('retry')}</Button></div>}
  </Stack>;
}

export function Toolbar({ label, ...props }: ComponentProps<'div'> & { label: string }) {
  return <div {...props} role="toolbar" aria-label={label} className={`flex flex-wrap items-center gap-2 ${props.className ?? ''}`} onKeyDown={(event) => {
    props.onKeyDown?.(event);
    if (event.defaultPrevented || !['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key) || !(event.target instanceof HTMLButtonElement)) return;
    const buttons = Array.from(event.currentTarget.querySelectorAll<HTMLButtonElement>('button:not(:disabled)'));
    const at = buttons.indexOf(event.target); if (at < 0) return; event.preventDefault();
    buttons[event.key === 'Home' ? 0 : event.key === 'End' ? buttons.length - 1 : (at + (event.key === 'ArrowLeft' ? -1 : 1) + buttons.length) % buttons.length]?.focus();
  }} />;
}
