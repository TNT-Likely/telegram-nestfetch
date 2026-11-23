import { useEffect, useMemo, useRef, useState } from 'react';
import type { FormEvent } from 'react';
import { ArrowUpRight, LoaderCircle, QrCode, Send, X } from 'lucide-react';
import QRCode from 'qrcode';
import type { Account, Failure } from './api';
import { failureText } from './i18n';
import type { Language, Translate } from './i18n';

interface Props {
  account: Account;
  busy: boolean;
  failure: Failure | undefined;
  t: Translate;
  language: Language;
  onClose: () => void;
  onConfigure: (credentials: { apiId: number; apiHash: string }) => Promise<boolean>;
  onLogin: () => void;
  onPassword: (password: string) => void;
  onRefresh: () => void;
  onSignOut: () => Promise<boolean>;
  onOpenSettings: () => void;
  onResetError: () => void;
}

function qrPng(value: string | undefined) {
  if (typeof value === 'string' && value.length <= 262_144 &&
      /^data:image\/png;base64,iVBORw0KGgo[A-Za-z0-9+/]*={0,2}$/.test(value)) {
    return value;
  }
}

function SignInCode({ url, image, expires, t }: { url?: string; image?: string; expires?: number; t: Translate }) {
  const [code, setCode] = useState<{ url: string; image: string }>();
  const [brokenImage, setBrokenImage] = useState<string>();
  const [now, setNow] = useState(() => Date.now());
  const fromEngine = useMemo(() => qrPng(image), [image]);
  const remaining = expires === undefined ? 0 : Math.max(0, Math.ceil(expires - now / 1000));
  const validTime = expires === undefined || remaining > 0;
  useEffect(() => {
    if (fromEngine || !url || !/^tg:\/\/login\?token=[a-z\d_=-]{1,2048}$/i.test(url)) return;
    let disposed = false;
    void QRCode.toDataURL(url, { width: 256, margin: 4, color: { dark: '#262725', light: '#ffffff' } })
      .then(image => { if (!disposed) setCode({ url, image }); })
      .catch(() => undefined);
    return () => { disposed = true; };
  }, [fromEngine, url]);
  useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now()), 500);
    return () => window.clearInterval(timer);
  }, []);
  const source = fromEngine || (code && code.url === url ? code.image : undefined);
  const visible = validTime && source && source !== brokenImage;
  return (
    <>
      <div className="qr" aria-busy={!visible}>
        {visible ? <img src={source} alt={t('qrLabel')} draggable={false} onError={() => setBrokenImage(source)} /> : <LoaderCircle className="spin" aria-hidden="true" />}
      </div>
      <p className="qr-timer">
        {visible && expires !== undefined ? t('qrTimer').replace('{seconds}', String(remaining)) : visible ? '' : t('qrRefreshing')}
      </p>
    </>
  );
}

export function AccountDialog({
  account, busy, failure, t, language, onClose, onConfigure, onLogin, onPassword,
  onRefresh, onSignOut, onOpenSettings, onResetError,
}: Props) {
  const dialog = useRef<HTMLDialogElement>(null);
  const [editing, setEditing] = useState(false);
  const [confirming, setConfirming] = useState(false);
  const [credentialsError, setCredentialsError] = useState(false);
  const password = useRef<HTMLInputElement>(null);

  useEffect(() => {
    const current = dialog.current;
    current?.showModal();
    return () => { if (current?.open) current.close(); };
  }, []);

  useEffect(() => {
    if (account.state === 'password') password.current?.focus();
  }, [account.state]);

  function submitCredentials(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    onResetError();
    const fields = new FormData(event.currentTarget);
    const rawId = String(fields.get('apiId') ?? '').trim();
    const apiId = Number(rawId);
    const apiHash = String(fields.get('apiHash') ?? '').trim();
    if (!/^\d+$/.test(rawId) || !Number.isSafeInteger(apiId) || apiId <= 0 || apiId > 2_147_483_647 || !/^[a-f\d]{32}$/i.test(apiHash)) {
      setCredentialsError(true);
      return;
    }
    setCredentialsError(false);
    void onConfigure({ apiId, apiHash }).then(success => { if (success) setEditing(false); });
  }

  function submitPassword(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const input = password.current;
    if (!input?.value) return;
    const value = input.value;
    input.value = '';
    onPassword(value);
  }

  const tdl = account.backend === 'tdl';
  const setup = !tdl && (editing || account.state === 'setup');
  const connected = account.state === 'connected';
  const signOutText = t(tdl ? 'disconnectTdl' : 'signOut');
  const title = setup ? t('configure') : connected ? (confirming ? t(tdl ? 'confirmDisconnectTdl' : 'confirmSignOut') : t('account'))
    : account.state === 'password' ? t('passwordTitle')
      : account.state === 'starting' || account.state === 'qr' ? t('qrTitle') : t('connect');
  const shownError = credentialsError ? { code: 'config_invalid' } : failure || (!editing ? account.error : undefined);
  const message = shownError && (
    <p className="dialog-error" role="alert">
      {tdl && shownError.code === 'password_invalid' ? t('tdlPasswordRetry') : failureText(language, shownError.code)}
    </p>
  );

  return (
    <dialog ref={dialog} className="account-dialog" aria-labelledby="dialog-title" onCancel={event => { event.preventDefault(); if (!busy) onClose(); }}>
      <div className="dialog-head">
        <h2 id="dialog-title">{title}</h2>
        <button className="icon-button" type="button" aria-label={t('close')} disabled={busy} onClick={onClose}>
          <X aria-hidden="true" />
        </button>
      </div>
      {setup ? (
        <>
          <p className="dialog-copy">{t('codeDescription')}</p>
          <button type="button" className="text-button" disabled={busy} onClick={onOpenSettings}>
            {t('openSettings')} <ArrowUpRight aria-hidden="true" />
          </button>
          <form className="dialog-form" onSubmit={submitCredentials} noValidate onChange={() => { setCredentialsError(false); onResetError(); }}>
            <label>
              <span>{t('apiId')}</span>
              <input name="apiId" inputMode="numeric" autoComplete="off" spellCheck={false} required disabled={busy} />
            </label>
            <label>
              <span>{t('apiHash')}</span>
              <input type="password" name="apiHash" autoComplete="off" spellCheck={false} required disabled={busy} />
            </label>
            {message}
            <div className="dialog-actions">
              {editing && <button type="button" className="secondary-button" disabled={busy} onClick={() => { setEditing(false); setCredentialsError(false); onResetError(); }}>{t('cancel')}</button>}
              <button type="submit" className="primary-button" disabled={busy}>{t('continue')}</button>
            </div>
          </form>
        </>
      ) : connected ? (
        confirming ? (
          <>
            <p className="dialog-copy">{t(tdl ? 'confirmDisconnectTdlDescription' : 'confirmSignOutDescription')}</p>
            {message}
            <div className="dialog-actions">
              <button className="secondary-button" type="button" disabled={busy} onClick={() => { setConfirming(false); onResetError(); }}>{t('cancel')}</button>
              <button className="primary-button" type="button" disabled={busy} onClick={() => { void onSignOut().then(success => { if (success) setConfirming(false); }); }}>{signOutText}</button>
            </div>
          </>
        ) : (
          <>
            <div className="session">
              <span className="session-icon"><Send aria-hidden="true" /></span>
              <div>
                <strong title={account.label}>{account.label || 'Telegram'}</strong>
                <span><i className="dot good" />{t('connected')}</span>
              </div>
            </div>
            <p className="dialog-copy">{t('sessionDescription')}</p>
            {message}
            <div className="dialog-actions">
              <button className="secondary-button" type="button" disabled={busy} onClick={() => setConfirming(true)}>{signOutText}</button>
              <button className="primary-button" type="button" disabled={busy} onClick={onClose}>{t('done')}</button>
            </div>
          </>
        )
      ) : account.state === 'password' ? (
        <>
          <p className="dialog-copy">{t('passwordDescription')}</p>
          <form className="dialog-form" onSubmit={submitPassword}>
            <label>
              <span>{t('enterPassword')}</span>
              <input type="password" ref={password} autoComplete="current-password" required disabled={busy} />
            </label>
            {message}
            <div className="dialog-actions">
              <button className="secondary-button" type="button" disabled={busy} onClick={onClose}>{t('cancel')}</button>
              <button className="primary-button" type="submit" disabled={busy}>{t('verify')}</button>
            </div>
          </form>
        </>
      ) : account.state === 'starting' || account.state === 'qr' ? (
        <>
          <p className="dialog-copy">{t('scanSteps')}</p>
          <SignInCode url={account.qrUrl} image={account.qrImage} expires={account.expires} t={t} />
          {message}
          <div className="dialog-actions">
            <button className="secondary-button" type="button" disabled={busy} onClick={onClose}>{t('cancel')}</button>
            {account.state === 'qr' && <button className="secondary-button" type="button" disabled={busy} onClick={onRefresh}>{t('refreshCode')}</button>}
          </div>
        </>
      ) : (
        <>
          <p className="dialog-copy">{t('connectDescription')}</p>
          {message}
          <div className="dialog-actions dialog-actions-wrap">
            {!tdl && <button className="secondary-button" type="button" disabled={busy} onClick={() => { setEditing(true); onResetError(); }}>{t('editCredentials')}</button>}
            <button className="primary-button" type="button" disabled={busy} onClick={onLogin}><QrCode aria-hidden="true" />{t('scan')}</button>
          </div>
        </>
      )}
    </dialog>
  );
}
