import { type ComponentProps, type ReactNode, useId } from 'react';
import { useTranslation } from 'react-i18next';
import { cn } from '../lib/utils.js';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from './select.js';

export interface SelectOption {
  value: string;
  label: ReactNode;
  disabled?: boolean;
}
export type SelectControlProps = Omit<
  ComponentProps<typeof SelectTrigger>,
  'children' | 'value' | 'defaultValue' | 'onChange'
> & {
  value: string;
  onValueChange: (value: string) => void;
  options: readonly SelectOption[];
  placeholder?: string;
};

/** A themed, controlled single choice. Empty values remain valid choices. */
export function SelectControl({
  value,
  onValueChange,
  options,
  placeholder,
  name,
  disabled,
  className,
  ...props
}: SelectControlProps) {
  const id = useId();
  const { t } = useTranslation('backplane.ui');
  let empty = `backplane-empty-${id}`;
  while (options.some((option) => option.value === empty)) empty += '-';
  const hasEmpty = options.some((option) => option.value === '');
  const selected = options.some((option) => option.value === value) ? (value === '' && hasEmpty ? empty : value) : '';
  return (
    <Select value={selected} onValueChange={(next) => onValueChange(next === empty ? '' : next)} disabled={disabled}>
      {name && <input type="hidden" name={name} value={value} disabled={disabled} />}
      <SelectTrigger {...props} disabled={disabled} className={cn('min-w-0 max-w-full', className)}>
        <SelectValue placeholder={placeholder ?? t('choose')} />
      </SelectTrigger>
      <SelectContent position="popper" align="start" sideOffset={4}>
        {options.map((option) => (
          <SelectItem
            key={option.value}
            data-value={option.value}
            value={option.value === '' ? empty : option.value}
            disabled={option.disabled}
          >
            {option.label}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  );
}
