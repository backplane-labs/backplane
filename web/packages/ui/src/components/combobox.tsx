import { useEffect, useId, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from './button.js';
import { Command, CommandEmpty, CommandInput, CommandItem, CommandList } from './command.js';
import { Input } from './input.js';
import { Popover, PopoverContent, PopoverTrigger } from './popover.js';

export interface ChoiceOption {
  value: string;
  label: string;
  disabled?: boolean;
  description?: string;
}
export interface ComboboxProps {
  label: string;
  options?: readonly ChoiceOption[];
  values: readonly string[];
  onValuesChange: (values: string[]) => void;
  multiple?: boolean;
  disabled?: boolean;
  placeholder?: string;
  loadOptions?: (query: string, signal: AbortSignal) => Promise<readonly ChoiceOption[]>;
}
export function Combobox({
  label,
  options = [],
  values,
  onValuesChange,
  multiple,
  disabled,
  placeholder,
  loadOptions,
}: ComboboxProps) {
  const { t } = useTranslation('backplane.ui');
  const [open, setOpen] = useState(false),
    [query, setQuery] = useState(''),
    [retry, setRetry] = useState(0);
  const [remote, setRemote] = useState<{ options: readonly ChoiceOption[]; loading: boolean; error: boolean }>({
    options: [],
    loading: false,
    error: false,
  });
  const labels = useRef(new Map<string, string>());
  const choices = loadOptions ? remote.options : options;
  for (const option of [...options, ...choices]) labels.current.set(option.value, option.label);
  useEffect(() => {
    if (!open || !loadOptions) return;
    const controller = new AbortController();
    setRemote({ options: [], loading: true, error: false });
    const timer = setTimeout(() => {
      Promise.resolve()
        .then(() => loadOptions(query, controller.signal))
        .then((result) => {
          if (!controller.signal.aborted) setRemote({ options: result, loading: false, error: false });
        })
        .catch(() => {
          if (!controller.signal.aborted) setRemote({ options: [], loading: false, error: true });
        });
    }, 150);
    return () => {
      clearTimeout(timer);
      controller.abort();
    };
  }, [query, loadOptions, open, retry]);
  const id = useId();
  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <Button
          type="button"
          variant="outline"
          disabled={disabled}
          role="combobox"
          aria-expanded={open}
          aria-controls={id}
          aria-label={label}
        >
          {values.length
            ? values.map((value) => labels.current.get(value) ?? value).join(', ')
            : (placeholder ?? t('choose'))}
        </Button>
      </PopoverTrigger>
      <PopoverContent className="p-0" align="start">
        <Command shouldFilter={!loadOptions}>
          <CommandInput
            aria-label={t('searchOptions', { name: label })}
            placeholder={t('search')}
            value={query}
            onValueChange={setQuery}
          />
          <CommandList id={id} aria-label={label} aria-multiselectable={multiple || undefined}>
            {remote.loading && loadOptions && (
              <div role="status" className="p-3">
                {t('loading')}
              </div>
            )}
            {!remote.loading && !remote.error && <CommandEmpty>{t('noOptions')}</CommandEmpty>}
            {remote.error && loadOptions && (
              <div className="p-3">
                <p role="alert">{t('optionsFailed')}</p>
                <Button type="button" variant="outline" onClick={() => setRetry((old) => old + 1)}>
                  {t('retry')}
                </Button>
              </div>
            )}
            {choices.map((option) => (
              <CommandItem
                key={option.value}
                value={option.value}
                keywords={[option.label]}
                disabled={option.disabled || disabled}
                onSelect={() => {
                  const next = multiple
                    ? values.includes(option.value)
                      ? values.filter((value) => value !== option.value)
                      : [...values, option.value]
                    : [option.value];
                  onValuesChange(next);
                  if (!multiple) setOpen(false);
                }}
              >
                <span aria-hidden="true">{values.includes(option.value) ? '✓' : ''}</span>
                <span>
                  {option.label}
                  {option.description && <small className="block text-muted-foreground">{option.description}</small>}
                </span>
                {values.includes(option.value) && <span className="sr-only">{t('selected')}</span>}
              </CommandItem>
            ))}
          </CommandList>
        </Command>
      </PopoverContent>
    </Popover>
  );
}

export function TagsInput({
  label,
  values,
  onValuesChange,
  disabled,
  readOnly,
}: {
  label: string;
  values: readonly string[];
  onValuesChange: (values: string[]) => void;
  disabled?: boolean;
  readOnly?: boolean;
}) {
  const { t } = useTranslation('backplane.ui');
  const [draft, setDraft] = useState('');
  const add = () => {
    const value = draft.trim();
    if (value && !values.includes(value)) onValuesChange([...values, value]);
    setDraft('');
  };
  return (
    <div className="flex flex-wrap items-center gap-2" role="group" aria-label={label}>
      {values.map((value) => (
        <span key={value} className="inline-flex items-center gap-1 rounded-md border px-2 py-1">
          {value}
          {!readOnly && (
            <Button
              type="button"
              variant="ghost"
              size="icon-sm"
              disabled={disabled}
              aria-label={t('removeValue', { name: value })}
              onClick={() => onValuesChange(values.filter((item) => item !== value))}
            >
              ×
            </Button>
          )}
        </span>
      ))}
      {!readOnly && (
        <>
          <Input
            aria-label={t('newTag', { name: label })}
            value={draft}
            disabled={disabled}
            onChange={(event) => setDraft(event.target.value)}
            onKeyDown={(event) => {
              if (event.key === 'Enter' && !event.nativeEvent.isComposing) {
                event.preventDefault();
                add();
              }
            }}
          />
          <Button type="button" variant="outline" disabled={disabled || !draft.trim()} onClick={add}>
            {t('add')}
          </Button>
        </>
      )}
    </div>
  );
}

export interface CascaderNode {
  value: string;
  label: string;
  children?: readonly CascaderNode[];
  disabled?: boolean;
}
export function Cascader({
  nodes,
  ...props
}: Omit<ComboboxProps, 'options' | 'loadOptions'> & { nodes: readonly CascaderNode[] }) {
  const options = useMemo(() => {
    const result: ChoiceOption[] = [];
    const visit = (items: readonly CascaderNode[], parents: string[], disabled = false) => {
      for (const item of items) {
        const labels = [...parents, item.label];
        if (item.children?.length) visit(item.children, labels, disabled || !!item.disabled);
        else result.push({ value: item.value, label: labels.join(' › '), disabled: disabled || item.disabled });
      }
    };
    visit(nodes, []);
    return result;
  }, [nodes]);
  return <Combobox {...props} options={options} />;
}
