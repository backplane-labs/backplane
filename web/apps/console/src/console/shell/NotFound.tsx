import { Link } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { MapPinOff } from 'lucide-react';
import { Button, EmptyState } from '@gopherex/backplane-ui';

export function NotFound() {
  const { t } = useTranslation('console');
  return <div className="rounded-lg border border-border bg-card">
    <EmptyState icon={<MapPinOff />} title={t('notFound')} description={t('notFoundHint')} action={<Button variant="outline" size="sm" asChild><Link to="/services">{t('back')}</Link></Button>} />
  </div>;
}
