import { Loader2Icon } from 'lucide-react';
import { useTranslation } from 'react-i18next';
import { cn } from '../lib/utils.js';

function Spinner({ className, ...props }: React.ComponentProps<'svg'>) {
  const { t } = useTranslation('backplane.ui');
  return (
    <Loader2Icon
      data-slot="spinner"
      role="status"
      aria-label={t('loading')}
      className={cn('size-4 animate-spin', className)}
      {...props}
    />
  );
}

export { Spinner };
