import { Toaster as Sonner, type ToasterProps } from 'sonner';

export { toast } from 'sonner';

export function Toaster(props: ToasterProps) {
  return (
    <Sonner
      {...props}
      toastOptions={{
        ...props.toastOptions,
        style: {
          background: 'var(--popover)',
          color: 'var(--popover-foreground)',
          border: '1px solid var(--border)',
          fontFamily: 'var(--font-sans)',
          ...props.toastOptions?.style,
        },
      }}
    />
  );
}
