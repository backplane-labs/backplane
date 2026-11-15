import { type ReactNode, useId } from 'react';
import {
  type Control,
  Controller,
  type ControllerProps,
  type ControllerRenderProps,
  type FieldPath,
  type FieldValues,
} from 'react-hook-form';
import { Field, FieldDescription, FieldError, FieldLabel } from './field.js';

export interface FormControlProps<T extends FieldValues> {
  control: Control<T>;
  name: FieldPath<T>;
  label: string;
  description?: string;
  rules?: ControllerProps<T, FieldPath<T>>['rules'];
  children: (
    field: ControllerRenderProps<T, FieldPath<T>>,
    accessibility: { id: string; 'aria-describedby': string; 'aria-invalid': boolean },
  ) => ReactNode;
}
/** Native RHF Controller integration without a second form-state abstraction. */
export function FormControl<T extends FieldValues>({
  control,
  name,
  label,
  description,
  rules,
  children,
}: FormControlProps<T>) {
  const id = useId();
  return (
    <Controller
      control={control}
      name={name}
      rules={rules}
      render={({ field, fieldState }) => (
        <Field data-invalid={fieldState.invalid}>
          <FieldLabel htmlFor={id}>{label}</FieldLabel>
          {children(field, {
            id,
            'aria-describedby': `${id}-description ${id}-error`,
            'aria-invalid': fieldState.invalid,
          })}
          <FieldDescription id={`${id}-description`}>{description}</FieldDescription>
          <FieldError id={`${id}-error`}>{fieldState.error?.message}</FieldError>
        </Field>
      )}
    />
  );
}
