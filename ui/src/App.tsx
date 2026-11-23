import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react';
import type { FormEvent } from 'react';
import { ArrowDownToLine, ChevronDown, ChevronRight, CircleCheck, Folder, Layers, Link2, Monitor, Moon, Send, Sun, X } from 'lucide-react';
import { AccountDialog } from './AccountDialog';
import { countByView, Downloads } from './Downloads';
import type { JobAction, View } from './Downloads';
import { chooseFolder, failureOf, openTelegramSettings, request, revealJob, setWindowTheme } from './api';
import type { Account, Failure, Job, Snapshot, Theme } from './api';
import { failureText, translator } from './i18n';
import type { Language, Translate } from './i18n';

const preferenceKey = 'tg-nestfetch.interface';
const appearanceChoices = [
  { key: 'light', Icon: Sun },
  { key: 'dark', Icon: Moon },
  { key: 'system', Icon: Monitor },
] as const;
const filters = [
  { key: 'all', Icon: Layers },
  { key: 'active', Icon: ArrowDownToLine },
  { key: 'completed', Icon: CircleCheck },
] as const;
const signingIn = new Set<Account['state']>(['starting', 'qr', 'password']);
const running = new Set<Job['state']>(['queued', 'resolving', 'downloading', 'saving']);

interface Preferences {
  language: Language;
  theme: Theme;
}

function getPreferences(): Preferences {
  const language = navigator.language.toLowerCase().startsWith('zh') ? 'zh' : 'en';
  try {
    const previous: Partial<Preferences> | null = JSON.parse(localStorage.getItem(preferenceKey) || 'null');
    return {
      language: previous?.language === 'zh' || previous?.language === 'en' ? previous.language : language,
      theme: previous?.theme === 'light' || previous?.theme === 'dark' ? previous.theme : 'system',
    };
  } catch {
    return { language, theme: 'system' };
  }
}

function useMediaMatch(query: string) {
  const [matches, setMatches] = useState(() => matchMedia(query).matches);
  useEffect(() => {
    const media = matchMedia(query);
    const update = () => setMatches(media.matches);
    media.addEventListener('change', update);
    update();
    return () => media.removeEventListener('change', update);
  }, [query]);
  return matches;
}

function usePreferences() {
  const [preferences, setPreferences] = useState(getPreferences);
  const systemDark = useMediaMatch('(prefers-color-scheme: dark)');
  useEffect(() => {
    try { localStorage.setItem(preferenceKey, JSON.stringify(preferences)); } catch {}
    document.documentElement.lang = preferences.language === 'zh' ? 'zh-CN' : 'en';
  }, [preferences]);
  useLayoutEffect(() => {
    document.documentElement.dataset.theme = preferences.theme === 'system'
      ? (systemDark ? 'dark' : 'light')
      : preferences.theme;
  }, [preferences.theme, systemDark]);
  useEffect(() => {
    void setWindowTheme(preferences.theme).catch(() => undefined);
  }, [preferences.theme]);
  return { preferences, setPreferences };
}

function useEngine() {
  const [snapshot, setSnapshot] = useState<Snapshot>();
  const [failure, setFailure] = useState<Failure>();
  const current = useRef<Snapshot | undefined>(undefined);
  const started = useRef(0);
  const applied = useRef(0);
  const pending = useRef<Promise<Snapshot | undefined> | null>(null);

  const refresh = useCallback(async (afterMutation = false) => {
    if (pending.current) {
      if (!afterMutation) return pending.current;
      await pending.current;
    }
    const version = ++started.current;
    const read = request('snapshot', undefined)
      .then(value => {
        if (version >= applied.current) {
          applied.current = version;
          current.current = value;
          setSnapshot(value);
          setFailure(undefined);
        }
        return value;
      })
      .catch(error => {
        if (version >= applied.current) {
          applied.current = version;
          setFailure(failureOf(error, 'engine_unavailable'));
        }
        return current.current;
      })
      .finally(() => { if (pending.current === read) pending.current = null; });
    pending.current = read;
    return read;
  }, []);

  useEffect(() => {
    let live = true;
    let timer: number | undefined;
    async function poll() {
      const value = await refresh();
      if (!live) return;
      const connected = value?.account.state === 'connected';
      const delay = value && (signingIn.has(value.account.state) || (connected && value.jobs.some(job => running.has(job.state)))) ? 650 : 1800;
      timer = window.setTimeout(poll, delay);
    }
    void poll();
    return () => {
      live = false;
      window.clearTimeout(timer);
    };
  }, [refresh]);

  return { snapshot, failure, refresh };
}

function ProjectMark() {
  return <span className="brand-mark" aria-hidden="true" />;
}

function ProductName({ t }: { t: Translate }) {
  return (
    <span className="product-name">
      <strong>TG NestFetch</strong>
      <small title={t('appVersion')}>v{import.meta.env.VITE_APP_VERSION}</small>
    </span>
  );
}

interface SidebarProps {
  view: View;
  jobs: Job[];
  account: Account | undefined;
  t: Translate;
  onView: (view: View) => void;
  onAccount: () => void;
}

function Sidebar({ view, jobs, account, t, onView, onAccount }: SidebarProps) {
  return (
    <aside className="sidebar">
      <div className="identity">
        <ProjectMark />
        <ProductName t={t} />
        <span>{t('subtitle')}</span>
      </div>
      <nav aria-label={t('filters')}>
        {filters.map(({ key, Icon }) => (
          <button className="nav-item" type="button" key={key} aria-pressed={view === key} onClick={() => onView(key)}>
            <Icon aria-hidden="true" />
            <span>{t(key)}</span>
            <span className="count">{countByView(jobs, key)}</span>
          </button>
        ))}
      </nav>
      <button className="account-button" type="button" aria-label={t('account')} aria-haspopup="dialog" disabled={!account} onClick={onAccount}>
        <span className="account-icon"><Send aria-hidden="true" /></span>
        <span className="account-caption">
          <strong title={account?.label}>{account?.label || 'Telegram'}</strong>
          <span><i className={account?.state === 'connected' ? 'dot good' : 'dot'} />{account ? t(account.state) : t('starting')}</span>
        </span>
        <ChevronRight className="account-chevron" aria-hidden="true" />
      </button>
    </aside>
  );
}

export function App() {
  const { preferences, setPreferences } = usePreferences();
  const compact = useMediaMatch('(max-width: 430px)');
  const t = translator(preferences.language);
  const { snapshot, failure: engineFailure, refresh } = useEngine();
  const [view, setView] = useState<View>('all');
  const [link, setLink] = useState('');
  const [busy, setBusy] = useState(false);
  const [accountOpen, setAccountOpen] = useState(false);
  const [appError, setAppError] = useState<Failure>();
  const [linkError, setLinkError] = useState<Failure>();
  const [accountError, setAccountError] = useState<Failure>();
  const [notice, setNotice] = useState<'added' | 'folderChanged'>();
  const input = useRef<HTMLInputElement>(null);
  const actionLock = useRef(false);
  const loginRequested = useRef(false);

  useEffect(() => {
    if (!notice) return;
    const timer = window.setTimeout(() => setNotice(undefined), 3500);
    return () => window.clearTimeout(timer);
  }, [notice]);

  async function run(task: () => Promise<void>, setError: (failure: Failure | undefined) => void = setAppError, fallback = 'unexpected') {
    if (actionLock.current) return false;
    actionLock.current = true;
    setBusy(true);
    setError(undefined);
    let succeeded = false;
    try {
      await task();
      succeeded = true;
    } catch (error) {
      setError(failureOf(error, fallback));
    } finally {
      await refresh(true);
      actionLock.current = false;
      setBusy(false);
    }
    return succeeded;
  }

  function openAccount() {
    setAccountError(undefined);
    setAccountOpen(true);
    const account = snapshot?.account;
    if (account && (account.preconfigured || account.backend === 'tdl') && account.state === 'disconnected' && !account.error) {
      void run(startLogin, setAccountError);
    }
  }

  async function startLogin() {
    await request('begin_login', undefined);
    loginRequested.current = true;
  }

  function dismissAccount() {
    loginRequested.current = false;
    setAccountOpen(false);
  }

  function closeAccount() {
    const state = snapshot?.account.state;
    if (state === 'qr' || state === 'password' || (state === 'starting' && loginRequested.current)) {
      void run(async () => {
        try {
          await request('cancel_login', undefined);
        } catch (error) {
          if (failureOf(error).code !== 'login_not_pending') throw error;
        }
      }, setAccountError)
        .then(success => { if (success) dismissAccount(); });
    } else {
      dismissAccount();
    }
  }

  async function addDownload(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const submitted = link;
    if (!submitted.trim()) {
      setLinkError({ code: 'invalid_link' });
      input.current?.focus();
      return;
    }
    const succeeded = await run(async () => { await request('enqueue', { url: submitted.trim() }); }, setLinkError);
    if (!succeeded) {
      input.current?.focus();
      return;
    }
    setLink(current => current === submitted ? '' : current);
    setView('all');
    setNotice('added');
    if (snapshot && snapshot.account.state !== 'connected') openAccount();
    else input.current?.focus();
  }

  async function setFolder() {
    await run(async () => {
      const folder = await chooseFolder(snapshot?.settings.folder || '');
      if (!folder) return;
      await request('set_folder', { folder });
      setNotice('folderChanged');
    }, setAppError, 'picker_failed');
  }

  function onJobAction(job: Job, action: JobAction) {
    void run(async () => {
      if (action === 'reveal') {
        await revealJob(job.id);
      } else if (action === 'save_elsewhere') {
        const folder = await chooseFolder(job.folder);
        if (folder) await request('save_elsewhere', { id: job.id, folder });
      } else {
        await request(action, { id: job.id });
      }
    }, setAppError, action === 'reveal' ? 'reveal_failed' : action === 'save_elsewhere' ? 'picker_failed' : 'unexpected');
  }

  const jobs = snapshot?.jobs ?? [];
  const account = snapshot?.account;
  const needsSetup = account?.state === 'setup' && account.backend !== 'tdl';
  const folder = snapshot?.settings.folder;
  const leaf = folder?.replace(/[/\\]+$/, '').split(/[/\\]/).pop();
  const folderName = leaf === 'Downloads' ? t('defaultFolder') : leaf || folder || t('defaultFolder');
  const displayedError = engineFailure || appError;
  const heading = view === 'all' ? t('downloads') : t(view);

  return (
    <div className="app">
      <Sidebar view={view} jobs={jobs} account={account} t={t} onView={setView} onAccount={openAccount} />
      <main className="workspace">
        <header className="top-bar">
          <div className="compact-brand"><ProjectMark /><ProductName t={t} /></div>
          <div className="preferences">
            <div className="switch" role="group" aria-label={t('appearance')}>
              {appearanceChoices.map(({ key, Icon }) => (
                <button key={key} type="button" title={t(key)} aria-label={t(key)} aria-pressed={preferences.theme === key} onClick={() => setPreferences({ ...preferences, theme: key })}>
                  <Icon aria-hidden="true" />
                </button>
              ))}
            </div>
            <div className="switch languages" role="group" aria-label={t('language')}>
              <button type="button" lang="zh-CN" aria-label="简体中文" aria-pressed={preferences.language === 'zh'} onClick={() => setPreferences({ ...preferences, language: 'zh' })}>中文</button>
              <button type="button" lang="en" aria-label="English" aria-pressed={preferences.language === 'en'} onClick={() => setPreferences({ ...preferences, language: 'en' })}>EN</button>
            </div>
          </div>
        </header>
        <div className="workspace-content">
          <h1>{heading}</h1>
          <form className="composer-form" onSubmit={addDownload} noValidate>
            <div className="composer" data-invalid={linkError?.code === 'invalid_link'}>
              <Link2 aria-hidden="true" />
              <input
                ref={input}
                type="text"
                value={link}
                onChange={event => { setLink(event.currentTarget.value); setLinkError(undefined); }}
                placeholder={t(compact ? 'linkShort' : 'linkPlaceholder')}
                aria-label={t('link')}
                aria-invalid={linkError?.code === 'invalid_link'}
                aria-describedby={linkError ? 'link-error' : undefined}
                inputMode="url"
                autoComplete="off"
                autoCapitalize="off"
                spellCheck={false}
              />
              <button className="primary-button download-button" type="submit" disabled={busy || !snapshot}>
                <ArrowDownToLine aria-hidden="true" /><span>{t('download')}</span>
              </button>
            </div>
            {linkError && <p className="form-error" id="link-error" role="alert">{failureText(preferences.language, linkError.code)}</p>}
            <button type="button" className="destination" title={folder || t('chooseFolder')} disabled={busy || !snapshot} onClick={() => { void setFolder(); }}>
              <Folder aria-hidden="true" /><span>{t('saveTo')}</span><b>{folderName}</b><ChevronDown aria-hidden="true" />
            </button>
          </form>
          {displayedError && (
            <div role="alert" className="error-banner">
              <span>{failureText(preferences.language, displayedError.code)}</span>
              {engineFailure ? (
                <button type="button" className="secondary-button" onClick={() => { void refresh(true); }}>{t('refresh')}</button>
              ) : (
                <button type="button" className="icon-button" aria-label={t('close')} onClick={() => setAppError(undefined)}><X aria-hidden="true" /></button>
              )}
            </div>
          )}
          {account && account.state !== 'connected' && (
            <div className="connect-banner">
              <span><Send aria-hidden="true" />{t(needsSetup ? 'setupDescription' : account.state === 'disconnected' ? 'connect' : account.state)}</span>
              <button className="secondary-button" type="button" aria-haspopup="dialog" onClick={openAccount}>{t(account.state === 'error' ? 'reconnect' : needsSetup ? 'configure' : 'scan')}</button>
            </div>
          )}
          <Downloads jobs={jobs} view={view} loading={!snapshot && !engineFailure} busy={busy} language={preferences.language} t={t} connected={account?.state === 'connected'} resumeRestarts={account?.backend === 'tdl'} onAction={onJobAction} />
        </div>
      </main>
      <div className="notifications" role="status" aria-live="polite">
        {notice && <span>{t(notice)}</span>}
      </div>
      {accountOpen && account && (
        <AccountDialog
          account={account}
          busy={busy}
          failure={accountError}
          language={preferences.language}
          t={t}
          onClose={closeAccount}
          onConfigure={credentials => run(async () => { await request('configure', credentials); await startLogin(); }, setAccountError)}
          onLogin={() => { void run(startLogin, setAccountError); }}
          onPassword={password => { void run(async () => { await request('login_password', { password }); }, setAccountError); }}
          onRefresh={() => { void run(async () => { await request('cancel_login', undefined); await startLogin(); }, setAccountError); }}
          onSignOut={() => run(async () => { await request('sign_out', undefined); }, setAccountError)}
          onOpenSettings={() => { void run(async () => { await openTelegramSettings(); }, setAccountError, 'open_failed'); }}
          onResetError={() => setAccountError(undefined)}
        />
      )}
    </div>
  );
}
