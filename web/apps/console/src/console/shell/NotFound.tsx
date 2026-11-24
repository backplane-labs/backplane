import { Button, EmptyState } from '@backplane-labs/backplane-ui';
import { MapPinOff } from 'lucide-react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router-dom';

export function NotFound() {
  const { t } = useTranslation('console');
  return (
    <div className="rounded-lg border border-border bg-card">
      <EmptyState
        icon={<MapPinOff />}
        title={<h1>{t('notFound')}</h1>}
        description={t('notFoundHint')}
        action={
          <Button variant="outline" size="sm" asChild>
            <Link to="/services">{t('back')}</Link>
          </Button>
        }
      />
    </div>
  );
}
