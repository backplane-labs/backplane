import { useRef, useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { ArrowRight, CircleAlert, Eye, EyeOff, KeyRound, LoaderCircle, Moon, Sun } from 'lucide-react';
import { Button, Input, Label } from '@gopherex/backplane-ui';
import { SessionHttpError, type BackplaneClient, type ConnectionState } from '@gopherex/backplane-client';
import type { ThemeMode } from '@gopherex/backplane-theme';
import { BrandMark } from './shell/BrandMark';
import './login.css';

export function Login({ client, state, mode, onThemeChange }: {
  client: BackplaneClient; state: ConnectionState; mode: ThemeMode; onThemeChange: (mode: ThemeMode) => void;
}) {
  const { t } = useTranslation('login');
  const [token, setToken] = useState(''), [visible, setVisible] = useState(false);
  const [pending, setPending] = useState(false), [error, setError] = useState<string>();
  const inFlight = useRef(false), input = useRef<HTMLInputElement>(null);
  const anonymous = state === 'anonymous', offline = state === 'offline' || state === 'disposed';
  async function submit(event: FormEvent) {
    event.preventDefault();
    if (inFlight.current || !token.trim()) return;
    inFlight.current = true; setPending(true); setError(undefined);
    try { await client.login(token); setToken(''); }
    catch (reason) {
      setError(reason instanceof SessionHttpError
        ? reason.status === 401 || reason.status === 403 ? 'invalid' : reason.status === 429 ? 'limited' : reason.status >= 500 ? 'unavailable' : 'failed'
        : 'unavailable');
      input.current?.focus();
    } finally { inFlight.current = false; setPending(false); }
  }
  return <div className="login-screen">
    <div className="login-theme"><Button type="button" variant="ghost" size="icon" aria-label={t(mode === 'dark' ? 'light' : 'dark')} title={t(mode === 'dark' ? 'light' : 'dark')} onClick={() => onThemeChange(mode === 'dark' ? 'light' : 'dark')}>{mode === 'dark' ? <Sun size={16} /> : <Moon size={16} />}</Button></div>
    <main className="login-main">
      <div className="login-brand"><span className="login-mark"><BrandMark size={28} /></span><span>{t('brand')}</span></div>
      <section className="login-panel" aria-labelledby="login-title">
        <div className="login-eyebrow">{t('console')}</div>
        <h1 id="login-title">{t('title')}</h1>
        <p className="login-description">{t('description')}</p>
        {anonymous ? <form className="login-form" onSubmit={submit} aria-busy={pending}>
          <div className="login-field">
            <Label htmlFor="operator-token">{t('token')}</Label>
            <div className="login-input-wrap">
              <KeyRound size={15} className="login-key" aria-hidden="true" />
              <Input ref={input} id="operator-token" name="token" type={visible ? 'text' : 'password'} autoComplete="current-password" autoCapitalize="none" spellCheck={false} autoFocus required
                placeholder={t('placeholder')} value={token} readOnly={pending} aria-invalid={error === 'invalid'} aria-describedby={`login-help${error ? ' login-error' : ''}`}
                onChange={(event) => { setToken(event.target.value); setError(undefined); }} />
              <Button className="login-visibility" type="button" variant="ghost" size="icon" aria-label={t(visible ? 'hide' : 'show')} aria-pressed={visible} onClick={() => setVisible((old) => !old)}>{visible ? <EyeOff size={16} /> : <Eye size={16} />}</Button>
            </div>
            <p id="login-help" className="login-help">{t('help')}</p>
          </div>
          {error && <div className="login-error" role="alert" id="login-error"><CircleAlert size={16} aria-hidden="true" /><span>{t(error)}</span></div>}
          <Button className="login-submit" type="submit" disabled={pending || !token.trim()}>{pending ? <><LoaderCircle size={15} className="login-spinner" aria-hidden="true" />{t('pending')}</> : <>{t('login')}<ArrowRight size={15} aria-hidden="true" /></>}</Button>
        </form> : <div className="login-session">
          {offline ? <><CircleAlert size={24} aria-hidden="true" /><h2>{t('offline')}</h2><p>{t('offlineHelp')}</p><Button variant="outline" onClick={() => { void client.start(); }}>{t('retry')}</Button></> : <p role="status"><LoaderCircle className="login-spinner" size={18} aria-hidden="true" />{t(state === 'checking' || state === 'idle' ? 'checking' : 'connecting')}</p>}
        </div>}
      </section>
      <p className="login-footer">{t('footer')}</p>
    </main>
  </div>;
}
