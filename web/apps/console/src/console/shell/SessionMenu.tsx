import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { create } from '@bufbuild/protobuf';
import { timestampDate } from '@bufbuild/protobuf/wkt';
import { KeyRound, LogOut, MonitorSmartphone, ShieldX, UserRound, Users } from 'lucide-react';
import { ListSessionsRequestSchema, RevokeOtherSessionsRequestSchema, RevokeSessionRequestSchema, SessionServiceClient, type Session } from '@gopherex/backplane-api';
import { useClient } from '@gopherex/backplane-react';
import { usePlatformAction, usePlatformQuery } from '@gopherex/backplane-platform-ui';
import {
  Button, ConfirmAction, DetailDrawer, DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuLabel, DropdownMenuSeparator, DropdownMenuTrigger,
  EmptyState, KeyValueList, Panel, StatusBadge, Timestamp,
} from '@gopherex/backplane-ui';

const date = (value?: Session['createdAt']) => value ? timestampDate(value) : undefined;

/** Operator session: current session details, every session with revoke, log out. */
export function SessionMenu({ onLogout, pending }: { onLogout: () => void; pending: boolean }) {
  const { t } = useTranslation('console'), client = useClient(SessionServiceClient);
  const [opened, setOpened] = useState(0), [manage, setManage] = useState(false);
  const sessions = usePlatformQuery(`sessions:${opened}`, (signal) => opened ? client.listSessions(create(ListSessionsRequestSchema), { signal }) : Promise.resolve(undefined));
  const revoke = usePlatformAction<unknown>();
  const current = sessions.value?.sessions.find((session) => session.current), others = (sessions.value?.sessions.length ?? 1) - 1;
  const control = (execute: (signal: AbortSignal) => Promise<unknown>) => async (signal: AbortSignal) => {
    const result = await revoke.run((abort) => execute(AbortSignal.any([signal, abort]))); if (result === undefined) throw new Error('Revoke outcome is unavailable'); sessions.refresh();
  };
  return <>
    <DropdownMenu onOpenChange={(open) => { if (open) setOpened((value) => value + 1); }}>
      <DropdownMenuTrigger asChild>
        <Button variant="ghost" size="icon" aria-label={t('session')} title={t('session')} disabled={pending}><UserRound size={17} /></Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-72">
        <DropdownMenuLabel className="flex items-center gap-2"><KeyRound className="size-3.5 text-link" />{t('operator')}</DropdownMenuLabel>
        <div className="grid gap-1 px-2 pb-2 text-xs text-muted-foreground">
          {current ? <>
            <span>{t('sessionStarted')} <Timestamp value={date(current.createdAt)} /></span>
            <span>{t('sessionExpires')} <Timestamp value={date(current.expiresAt)} /></span>
            {current.address && <span className="font-mono">{current.address}</span>}
          </> : <span>{t(sessions.error ? 'sessionsFailed' : 'loadingShort')}</span>}
        </div>
        <DropdownMenuSeparator />
        <DropdownMenuItem onSelect={() => setManage(true)}><Users />{t('manageSessions')}</DropdownMenuItem>
        <DropdownMenuItem disabled={others <= 0 || revoke.pending || revoke.disabled} onSelect={() => { void revoke.run((signal) => client.revokeOtherSessions(create(RevokeOtherSessionsRequestSchema), { signal })).then(sessions.refresh); }}>
          {others > 0 ? <ShieldX /> : <MonitorSmartphone />}{others > 0 ? t('revokeOthers', { count: others }) : t('noOtherSessions')}
        </DropdownMenuItem>
        <DropdownMenuSeparator />
        <DropdownMenuItem variant="destructive" onSelect={onLogout}><LogOut />{t('logout')}</DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
    <DetailDrawer open={manage} onOpenChange={(open) => { setManage(open); if (open) setOpened((value) => value + 1); }} title={t('sessions')} description={t('sessionsHelp')}>
      {revoke.error !== undefined && <p className="m-0 mb-3 text-xs text-destructive" role="alert">{t('revokeFailed')}</p>}
      <div className="grid gap-3">
        {!sessions.value?.sessions.length && <EmptyState title={t(sessions.error ? 'sessionsFailed' : 'loadingShort')} />}
        {sessions.value?.sessions.map((session) => <Panel key={session.id} title={<span className="font-mono text-xs">{session.id.slice(0, 8)}</span>}
          actions={session.current ? <StatusBadge tone="accent">{t('thisSession')}</StatusBadge>
            : <ConfirmAction trigger={t('revoke')} title={t('revokeConfirm')} description={t('revokeHelp')} disabled={revoke.pending || revoke.disabled}
              onConfirm={control((signal) => client.revokeSession(create(RevokeSessionRequestSchema, { id: session.id }), { signal }))} />}>
          <KeyValueList items={[
            { label: t('sessionStarted'), value: <Timestamp value={date(session.createdAt)} absolute /> },
            { label: t('lastSeen'), value: <Timestamp value={date(session.lastSeenAt)} /> },
            { label: t('sessionExpires'), value: <Timestamp value={date(session.expiresAt)} /> },
            { label: t('address'), value: session.address || '—', mono: true },
            { label: t('client'), value: session.userAgent || '—' },
          ]} />
        </Panel>)}
      </div>
    </DetailDrawer>
  </>;
}
