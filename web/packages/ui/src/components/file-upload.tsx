import { useEffect, useId, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from './button.js';
import { Input } from './input.js';
import { Progress } from './progress.js';

export interface FileUploadProps {
  label: string;
  accept?: string;
  maxBytes?: number;
  maxFiles?: number;
  disabled?: boolean;
  upload: (file: File, context: { signal: AbortSignal; progress: (percent: number) => void }) => Promise<void>;
}
type Entry = {
  id: string;
  file: File;
  percent: number;
  status: 'uploading' | 'uploaded' | 'failed' | 'cancelled' | 'rejected';
};
export function FileUpload({
  label,
  accept,
  maxBytes = 50 * 1024 * 1024,
  maxFiles = 10,
  disabled,
  upload,
}: FileUploadProps) {
  const { t } = useTranslation('backplane.ui');
  const id = useId();
  const [entries, setEntries] = useState<Entry[]>([]),
    [over, setOver] = useState(false),
    [limit, setLimit] = useState(false);
  const snapshot = useRef(entries);
  const active = useRef(new Map<string, AbortController>());
  const mounted = useRef(true);
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
      for (const controller of active.current.values()) controller.abort();
      active.current.clear();
    };
  }, []);
  const replace = (next: Entry[]) => {
    snapshot.current = next;
    if (mounted.current) setEntries(next);
  };
  const patch = (key: string, changes: Partial<Entry>) =>
    replace(snapshot.current.map((entry) => (entry.id === key ? { ...entry, ...changes } : entry)));
  const matches = (file: File) =>
    !accept ||
    accept.split(',').some((part) => {
      const pattern = part.trim().toLowerCase();
      return pattern.startsWith('.')
        ? file.name.toLowerCase().endsWith(pattern)
        : pattern.endsWith('/*')
          ? file.type.startsWith(pattern.slice(0, -1))
          : file.type === pattern;
    });
  const start = async (entry: Entry) => {
    if (active.current.has(entry.id)) return;
    const controller = new AbortController();
    active.current.set(entry.id, controller);
    patch(entry.id, { status: 'uploading', percent: 0 });
    try {
      await upload(entry.file, {
        signal: controller.signal,
        progress: (percent) => {
          if (!controller.signal.aborted && Number.isFinite(percent))
            patch(entry.id, { percent: Math.max(0, Math.min(100, percent)) });
        },
      });
      if (!controller.signal.aborted) patch(entry.id, { status: 'uploaded', percent: 100 });
    } catch {
      if (!controller.signal.aborted) patch(entry.id, { status: 'failed' });
    } finally {
      if (active.current.get(entry.id) === controller) active.current.delete(entry.id);
    }
  };
  const add = (files: File[]) => {
    if (disabled) return;
    const capacity = Math.max(0, maxFiles - snapshot.current.length);
    setLimit(files.length > capacity);
    const additions = files.slice(0, capacity).map(
      (file): Entry => ({
        id: crypto.randomUUID(),
        file,
        percent: 0,
        status: file.size > maxBytes || !matches(file) ? 'rejected' : 'uploading',
      }),
    );
    replace([...snapshot.current, ...additions]);
    for (const entry of additions) if (entry.status !== 'rejected') void start(entry);
  };
  return (
    <section aria-label={label} className="flex flex-col gap-3">
      <div
        className="rounded-md border border-dashed p-4"
        style={{ background: over ? 'var(--accent)' : undefined }}
        onDragOver={(event) => {
          event.preventDefault();
          if (!disabled) setOver(true);
        }}
        onDragLeave={() => setOver(false)}
        onDrop={(event) => {
          event.preventDefault();
          setOver(false);
          add(Array.from(event.dataTransfer.files));
        }}
      >
        <label htmlFor={id}>
          {label} — {t('dropFiles')}
        </label>
        <Input
          id={id}
          type="file"
          accept={accept}
          multiple={maxFiles > 1}
          disabled={disabled}
          onChange={(event) => {
            add(Array.from(event.target.files ?? []));
            event.target.value = '';
          }}
        />
      </div>
      {limit && <p role="alert">{t('fileLimit', { count: maxFiles })}</p>}
      <ul className="flex flex-col gap-3">
        {entries.map((entry) => (
          <li key={entry.id} className="flex flex-col gap-2 rounded-md border p-3">
            <span>
              {entry.file.name} · {entry.file.size} B
            </span>
            <span role="status">{t(`file_${entry.status}`)}</span>
            {entry.status === 'uploading' && (
              <Progress aria-label={t('uploadProgress', { name: entry.file.name })} value={entry.percent} />
            )}
            <div className="flex gap-2">
              {entry.status === 'uploading' && (
                <Button
                  type="button"
                  variant="outline"
                  onClick={() => {
                    active.current.get(entry.id)?.abort();
                    active.current.delete(entry.id);
                    patch(entry.id, { status: 'cancelled' });
                  }}
                >
                  {t('cancel')}
                </Button>
              )}
              {['failed', 'cancelled'].includes(entry.status) && (
                <Button type="button" variant="outline" disabled={disabled} onClick={() => void start(entry)}>
                  {t('retry')}
                </Button>
              )}
              {entry.status !== 'uploading' && (
                <Button
                  type="button"
                  variant="ghost"
                  onClick={() => replace(snapshot.current.filter((item) => item.id !== entry.id))}
                >
                  {t('removeValue', { name: entry.file.name })}
                </Button>
              )}
            </div>
          </li>
        ))}
      </ul>
    </section>
  );
}
