import { Check, ChevronDown, X } from 'lucide-react';
import { type ReactNode, useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { cn } from '../lib/utils.js';
import { Command, CommandEmpty, CommandGroup, CommandInput, CommandItem, CommandList } from './command.js';
import { Popover, PopoverContent, PopoverTrigger } from './popover.js';

export interface FilterOption {
  value: string;
  label?: string;
  hint?: ReactNode;
}

/**
 * Compact filter: pick one of the known values, search them, or type any
 * value (`allowCustom`). `loadOptions` fetches values when the list opens.
 */
export function FilterCombo({
  label,
  value,
  onChange,
  options = [],
  loadOptions,
  allowCustom = true,
  placeholder,
  icon,
  className,
  mono,
}: {
  label: string;
  value: string;
  onChange: (value: string) => void;
  options?: readonly (FilterOption | string)[];
  loadOptions?: (signal: AbortSignal) => Promise<readonly (FilterOption | string)[]>;
  allowCustom?: boolean;
  placeholder?: string;
  icon?: ReactNode;
  className?: string;
  mono?: boolean;
}) {
  const { t } = useTranslation('backplane.ui');
  const [open, setOpen] = useState(false),
    [query, setQuery] = useState('');
  const [loaded, setLoaded] = useState<{
    options: readonly (FilterOption | string)[];
    loading: boolean;
    error: boolean;
  }>({ options: [], loading: false, error: false });
  useEffect(() => {
    if (!open || !loadOptions) return;
    const controller = new AbortController();
    setLoaded((old) => ({ ...old, loading: true, error: false }));
    loadOptions(controller.signal).then(
      (result) => {
        if (!controller.signal.aborted) setLoaded({ options: result, loading: false, error: false });
      },
      () => {
        if (!controller.signal.aborted) setLoaded({ options: [], loading: false, error: true });
      },
    );
    return () => controller.abort();
  }, [open, loadOptions]);
  const all = [...options, ...loaded.options].map((option) =>
    typeof option === 'string' ? { value: option } : option,
  );
  const unique = [...new Map(all.map((option) => [option.value, option])).values()];
  const typed = query.trim();
  return (
    <Popover
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        if (!next) setQuery('');
      }}
    >
      <div
        data-slot="filter-combo"
        className={cn(
          'inline-flex h-8 max-w-64 min-w-0 items-center rounded-md border border-input bg-background text-xs dark:bg-input/30',
          value && 'border-primary/50',
          className,
        )}
      >
        <PopoverTrigger asChild>
          <button
            type="button"
            role="combobox"
            aria-expanded={open}
            aria-label={label}
            className="inline-flex h-full min-w-0 flex-1 items-center gap-1.5 rounded-md px-2 text-left outline-none focus-visible:ring-2 focus-visible:ring-ring [&_svg]:size-3.5 [&_svg]:shrink-0"
          >
            {icon}
            <span className="shrink-0 text-muted-foreground">
              {label}
              {value ? ':' : ''}
            </span>
            <span className={cn('min-w-0 truncate', mono && 'font-mono', !value && 'text-muted-foreground')}>
              {value ? (unique.find((option) => option.value === value)?.label ?? value) : (placeholder ?? t('any'))}
            </span>
            {!value && <ChevronDown className="ml-auto text-muted-foreground" />}
          </button>
        </PopoverTrigger>
        {value && (
          <button
            type="button"
            aria-label={t('clearFilter', { name: label })}
            onClick={() => onChange('')}
            className="mr-1 grid size-5 shrink-0 place-items-center rounded-sm text-muted-foreground hover:bg-raised hover:text-foreground [&_svg]:size-3"
          >
            <X />
          </button>
        )}
      </div>
      <PopoverContent align="start" className="w-72 p-0">
        <Command>
          <CommandInput placeholder={t('searchOptions', { name: label })} value={query} onValueChange={setQuery} />
          <CommandList>
            <CommandEmpty>
              {loaded.loading ? t('loading') : loaded.error ? t('optionsFailed') : t('noOptions')}
            </CommandEmpty>
            {allowCustom && typed && !unique.some((option) => option.value === typed) && (
              <CommandGroup>
                <CommandItem
                  value={`__custom ${typed}`}
                  onSelect={() => {
                    onChange(typed);
                    setOpen(false);
                  }}
                >
                  {t('useValue', { value: typed })}
                </CommandItem>
              </CommandGroup>
            )}
            <CommandGroup>
              {unique.map((option) => (
                <CommandItem
                  key={option.value}
                  value={`${option.value} ${option.label ?? ''}`}
                  onSelect={() => {
                    onChange(option.value === value ? '' : option.value);
                    setOpen(false);
                  }}
                >
                  <Check className={cn('size-3.5', option.value === value ? 'opacity-100' : 'opacity-0')} />
                  <span className={cn('min-w-0 flex-1 truncate', mono && 'font-mono text-xs')}>
                    {option.label ?? option.value}
                  </span>
                  {option.hint && <span className="shrink-0 text-2xs text-muted-foreground">{option.hint}</span>}
                </CommandItem>
              ))}
            </CommandGroup>
          </CommandList>
        </Command>
      </PopoverContent>
    </Popover>
  );
}
