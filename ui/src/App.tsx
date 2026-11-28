import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react';
import type { FormEvent } from 'react';
import { ArrowDownToLine, Bookmark, ChevronDown, ChevronRight, CircleCheck, Eye, Folder, Layers, Library, Link2, LoaderCircle, Monitor, Moon, Play, Send, Settings, Sun, X } from 'lucide-react';
import { AccountDialog } from './AccountDialog';
import { ChannelAttachments } from './ChannelAttachments';
import { countByView, Downloads } from './Downloads';
import type { JobAction, JobActionOptions, View } from './Downloads';
import { duration, filesize, MediaArtwork } from './MediaArtwork';
import { SettingsDialog } from './SettingsDialog';
import { VideoPreview } from './VideoPreview';
import type { PreviewTarget, TelegramPreviewTarget } from './VideoPreview';
import { chooseFolder, failureOf, imageSource, openProjectRepository, openTelegramSettings, request, revealJob, setWindowTheme, startTask } from './api';
import type { Account, BatchResult, Failure, Job, LinkPreview, Snapshot, Theme } from './api';
import { failureText, translator } from './i18n';
import type { Language, Translate } from './i18n';
import githubMark from './github-mark.svg?no-inline';

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
const reservedTelegramUsernames = new Set([
  'addemoji', 'addlist', 'addstickers', 'addtheme', 'boost', 'c', 'contact',
  'giftcode', 'invoice', 'joinchat', 'login', 'm', 'msg', 'nft', 'proxy', 's',
  'setlanguage', 'share', 'socks',
]);
type WorkspaceView = View | 'channels' | 'saved';
type LinkInspection =
  | { url: string; state: 'loading'; waiting: boolean }
  | { url: string; state: 'ready'; preview: LinkPreview }
  | { url: string; state: 'failed'; error: Failure };

function looksLikeTelegramLink(raw: string) {
  const trimmed = raw.trim();
  if (!trimmed || trimmed.length > 2048) return false;
  const candidate = /^t\.me\//i.test(trimmed) ? `https://${trimmed}` : trimmed;
  let parsed: URL;
  try {
    parsed = new URL(candidate);
  } catch {
    return false;
  }
  if (parsed.protocol !== 'https:' || parsed.hostname.toLowerCase() !== 't.me' || parsed.username || parsed.password || parsed.hash) {
    return false;
  }
  for (const key of parsed.searchParams.keys()) {
    if (key !== 'single' && key !== 't' && key !== 'thread') return false;
  }
  const parts = parsed.pathname.replace(/^\/+|\/+$/g, '').split('/').filter(Boolean);
  if (parts[0] === 'c') {
    return (parts.length === 3 || parts.length === 4) && parts.slice(1).every(part => /^\d+$/.test(part) && Number(part) > 0);
  }
  const offset = parts[0] === 's' ? 1 : 0;
  const rest = parts.slice(offset);
  if (rest.length !== 2 && rest.length !== 3) return false;
  const username = rest[0].toLowerCase();
  if (!/^[a-z0-9_]{1,32}$/.test(username) || reservedTelegramUsernames.has(username)) return false;
  return rest.slice(1).every(part => /^\d+$/.test(part) && Number(part) > 0);
}

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
  view: WorkspaceView;
  jobs: Job[];
  account: Account | undefined;
  t: Translate;
  onView: (view: WorkspaceView) => void;
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
        <button className="nav-item channel-nav" type="button" aria-pressed={view === 'channels'} onClick={() => onView('channels')}>
          <Library aria-hidden="true" /><span>{t('channelAttachments')}</span>
        </button>
        <button className="nav-item" type="button" aria-pressed={view === 'saved'} onClick={() => onView('saved')}>
          <Bookmark aria-hidden="true" /><span>{t('savedMessages')}</span>
        </button>
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
  const [view, setView] = useState<WorkspaceView>('all');
  const browsingAttachments = view === 'channels' || view === 'saved';
  const [link, setLink] = useState('');
  const [busy, setBusy] = useState(false);
  const [accountOpen, setAccountOpen] = useState(false);
  const [settingsOpen, setSettingsOpen] = useState(false);
  const [appError, setAppError] = useState<Failure>();
  const [linkError, setLinkError] = useState<Failure>();
  const [accountError, setAccountError] = useState<Failure>();
  const [notice, setNotice] = useState<'added' | 'folderChanged' | 'downloadHidden' | 'downloadRestored' | 'downloadRemoved' | BatchResult>();
  const [preview, setPreview] = useState<PreviewTarget>();
  const [inspection, setInspection] = useState<LinkInspection>();
  const input = useRef<HTMLInputElement>(null);
  const actionLock = useRef(false);
  const loginRequested = useRef(false);
  const stopInspect = useRef<(() => Promise<void>) | undefined>(undefined);
  const inspectNonce = useRef(0);
  const inspectedUrl = useRef('');
  const dismissedUrl = useRef('');

  const cancelInspect = useCallback((clear = true) => {
    inspectNonce.current++;
    const stop = stopInspect.current;
    stopInspect.current = undefined;
    void stop?.();
    if (clear) {
      inspectedUrl.current = '';
      setInspection(undefined);
    }
  }, []);

  const beginInspect = useCallback((rawUrl: string) => {
    const trimmed = rawUrl.trim();
    if (!trimmed) {
      setLinkError({ code: 'invalid_link' });
      input.current?.focus();
      return;
    }
    dismissedUrl.current = '';
    cancelInspect(false);
    const nonce = inspectNonce.current;
    inspectedUrl.current = trimmed;
    setLinkError(undefined);
    setInspection({ url: trimmed, state: 'loading', waiting: false });
    const task = startTask(
      () => request('inspect_start', { url: trimmed }),
      id => request('inspect_status', id),
      id => request('browse_cancel', id),
      waiting => {
        if (inspectNonce.current !== nonce) return;
        setInspection(current => current?.state === 'loading' && current.url === trimmed ? { ...current, waiting } : current);
      },
    );
    stopInspect.current = task.stop;
    void task.result.then(status => {
      if (inspectNonce.current !== nonce || !status?.preview) return;
      setInspection({ url: trimmed, state: 'ready', preview: status.preview });
    }).catch(error => {
      if (inspectNonce.current !== nonce) return;
      const failure = failureOf(error);
      if (failure.code === 'invalid_link') {
        inspectedUrl.current = '';
        setInspection(undefined);
        setLinkError(failure);
      } else {
        setInspection({ url: trimmed, state: 'failed', error: failure });
      }
    }).finally(() => {
      if (stopInspect.current === task.stop) stopInspect.current = undefined;
      void task.stop();
    });
  }, [cancelInspect]);

  const connected = snapshot?.account.state === 'connected';
  useEffect(() => {
    const trimmed = link.trim();
    if (browsingAttachments || !connected || !trimmed || !looksLikeTelegramLink(trimmed)) {
      if (inspectedUrl.current && inspectedUrl.current !== trimmed) cancelInspect(true);
      else if (browsingAttachments || !connected || !trimmed) cancelInspect(true);
      return;
    }
    if (inspectedUrl.current === trimmed || dismissedUrl.current === trimmed) return;
    const timer = window.setTimeout(() => { beginInspect(trimmed); }, 220);
    return () => window.clearTimeout(timer);
  }, [link, connected, browsingAttachments, beginInspect, cancelInspect]);

  useEffect(() => () => { cancelInspect(false); }, [cancelInspect]);

  useEffect(() => {
    if (!notice) return;
    const timer = window.setTimeout(() => setNotice(undefined), 3500);
    return () => window.clearTimeout(timer);
  }, [notice]);

  useEffect(() => {
    if (accountOpen) setPreview(undefined);
    else if (snapshot?.account.state !== 'connected') setPreview(current => current?.jobId ? current : undefined);
  }, [snapshot?.account.state, accountOpen]);

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

  async function enqueueLink(rawUrl: string) {
    const trimmed = rawUrl.trim();
    if (!trimmed) {
      setLinkError({ code: 'invalid_link' });
      input.current?.focus();
      return;
    }
    cancelInspect(true);
    const succeeded = await run(async () => { await request('enqueue', { url: trimmed }); }, setLinkError);
    if (!succeeded) {
      input.current?.focus();
      return;
    }
    dismissedUrl.current = '';
    setLink(current => current.trim() === trimmed ? '' : current);
    setView('all');
    setNotice('added');
    if (snapshot && snapshot.account.state !== 'connected') openAccount();
    else input.current?.focus();
  }

  async function addDownload(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const trimmed = link.trim();
    if (!trimmed) {
      setLinkError({ code: 'invalid_link' });
      input.current?.focus();
      return;
    }
    if (!connected) {
      await enqueueLink(trimmed);
      return;
    }
    if (inspection?.url === trimmed && inspection.state === 'ready') {
      const alreadyQueued = Boolean(inspection.preview.duplicate) || jobs.some(job =>
        job.url === inspection.preview.url && job.state !== 'completed');
      if (alreadyQueued) {
        setLinkError({ code: 'duplicate' });
        return;
      }
      await enqueueLink(trimmed);
      return;
    }
    if (inspection?.url === trimmed && inspection.state === 'loading') return;
    beginInspect(trimmed);
  }

  function openPreview(target: TelegramPreviewTarget) {
    if (snapshot?.account.state === 'connected') {
      if (inspection?.state === 'loading') cancelInspect(true);
      setPreview(target);
    } else {
      openAccount();
    }
  }

  function playDownload(job: Job, thumbnail: string | undefined) {
    setPreview({ jobId: job.id, name: job.title || job.name, thumbnail });
  }

  async function addFromPreview(url: string) {
    let issue: Failure | undefined;
    const succeeded = await run(async () => { await request('enqueue', { url }); }, failure => { issue = failure; });
    if (!succeeded) return issue ?? { code: 'busy' };
    if (!browsingAttachments) {
      cancelInspect(true);
      dismissedUrl.current = '';
      setView('all');
      setLink(current => current.trim() === url ? '' : current);
    }
    setNotice('added');
  }

  function addBatch(urls: string[]) {
    return run(async () => { setNotice(await request('enqueue_batch', { urls })); });
  }

  async function setFolder() {
    await run(async () => {
      const folder = await chooseFolder(snapshot?.settings.folder || '');
      if (!folder) return;
      await request('set_folder', { folder });
      setNotice('folderChanged');
    }, setAppError, 'picker_failed');
  }

  async function onJobAction(job: Job, action: JobAction, options?: JobActionOptions) {
    const succeeded = await run(async () => {
      if (action === 'reveal') {
        await revealJob(job.id);
      } else if (action === 'save_elsewhere') {
        const folder = await chooseFolder(job.folder);
        if (folder) await request('save_elsewhere', { id: job.id, folder });
      } else if (action === 'remove_job') {
        await request(action, { id: job.id, discardStaged: options?.discardStaged });
      } else {
        await request(action, { id: job.id });
      }
    }, options?.onError ?? setAppError, action === 'reveal' ? 'reveal_failed' : action === 'save_elsewhere' ? 'picker_failed' : 'unexpected');
    if (succeeded) {
      if (action === 'hide_job') setNotice('downloadHidden');
      if (action === 'restore_job') setNotice('downloadRestored');
      if (action === 'remove_job') setNotice('downloadRemoved');
    }
    return succeeded;
  }

  const jobs = snapshot?.jobs ?? [];
  const account = snapshot?.account;
  const trimmedLink = link.trim();
  const activeInspection = inspection && inspection.url === trimmedLink ? inspection : undefined;
  const inspectedPreview = activeInspection?.state === 'ready' ? activeInspection.preview : undefined;
  const inspectedDuplicate = Boolean(inspectedPreview && (inspectedPreview.duplicate || jobs.some(job =>
    job.url === inspectedPreview.url && job.state !== 'completed')));
  const inspectedDuration = inspectedPreview ? duration(inspectedPreview.duration) : null;
  const inspectedTitle = inspectedPreview ? (inspectedPreview.title || inspectedPreview.name || t('telegramAttachment')) : '';
  const inspectedMeta = inspectedPreview
    ? [t(inspectedPreview.kind), filesize(inspectedPreview.size), inspectedPreview.source].filter(Boolean).join(' · ')
    : '';
  const canConfirmPreview = Boolean(inspectedPreview && !inspectedDuplicate);
  const needsSetup = account?.state === 'setup' && account.backend !== 'tdl';
  const folder = snapshot?.settings.folder;
  const leaf = folder?.replace(/[/\\]+$/, '').split(/[/\\]/).pop();
  const folderName = leaf === 'Downloads' ? t('defaultFolder') : leaf || folder || t('defaultFolder');
  const displayedError = engineFailure || appError;
  const heading = t(view === 'channels' ? 'channelAttachments' : view === 'saved' ? 'savedMessages' : view === 'all' ? 'downloads' : view);
  const destination = (
    <button type="button" className="destination" title={folder || t('chooseFolder')} disabled={busy || !snapshot} onClick={() => { void setFolder(); }}>
      <Folder aria-hidden="true" /><span>{t('saveTo')}</span><b>{folderName}</b><ChevronDown aria-hidden="true" />
    </button>
  );
  const notification = typeof notice === 'string' ? t(notice) : notice && [
    notice.added > 0 && t('batchAdded').replace('{count}', String(notice.added)),
    notice.duplicates > 0 && t('batchDuplicates').replace('{count}', String(notice.duplicates)),
  ].filter(Boolean).join(' ');

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
            <button className="project-link settings-button" type="button" title={t('appSettings')} aria-label={t('appSettings')}
              aria-haspopup="dialog" onClick={() => setSettingsOpen(true)}>
              <Settings aria-hidden="true" />
            </button>
            <button className="project-link" type="button" title={t('projectRepository')} aria-label={t('projectRepository')}
              onClick={() => { void openProjectRepository().catch(error => setAppError(failureOf(error, 'open_failed'))); }}>
              <img src={githubMark} alt="" width="16" height="16" />
            </button>
          </div>
        </header>
        <div className="workspace-content">
          <h1>{heading}</h1>
          {!browsingAttachments && <form className="composer-form" onSubmit={addDownload} noValidate>
            <div className="composer" data-invalid={linkError?.code === 'invalid_link'}>
              <Link2 aria-hidden="true" />
              <input
                ref={input}
                type="text"
                value={link}
                onChange={event => {
                  const next = event.currentTarget.value;
                  if (dismissedUrl.current && dismissedUrl.current !== next.trim()) dismissedUrl.current = '';
                  setLink(next);
                  setLinkError(undefined);
                }}
                placeholder={t(compact ? 'linkShort' : 'linkPlaceholder')}
                aria-label={t('link')}
                aria-invalid={linkError?.code === 'invalid_link'}
                aria-describedby={linkError ? 'link-error' : undefined}
                inputMode="url"
                autoComplete="off"
                autoCapitalize="off"
                spellCheck={false}
              />
              <button className="preview-button row-action" type="button" aria-label={t('preview')} title={t('preview')} aria-haspopup="dialog" disabled={busy || !snapshot || !trimmedLink} onClick={() => openPreview({
                url: trimmedLink,
                name: inspectedPreview ? inspectedTitle : undefined,
                thumbnail: inspectedPreview?.thumbnail,
              })}>
                <Play aria-hidden="true" /><span>{t('preview')}</span>
              </button>
              <button className="primary-button download-button" type="submit" disabled={busy || !snapshot || activeInspection?.state === 'loading' || (Boolean(inspectedPreview) && inspectedDuplicate)}>
                {connected && !inspectedPreview ? <Eye aria-hidden="true" /> : <ArrowDownToLine aria-hidden="true" />}
                <span>{t(!connected ? 'download' : inspectedPreview ? 'confirmDownload' : 'inspectLink')}</span>
              </button>
            </div>
            {linkError && <p className="form-error" id="link-error" role="alert">{failureText(preferences.language, linkError.code)}</p>}
            {activeInspection && (
              <div className="link-preview" role="region" aria-label={t('inspectReady')} aria-busy={activeInspection.state === 'loading'}>
                {activeInspection.state === 'loading' ? (
                  <>
                    <div className="link-preview-status" role="status">
                      <LoaderCircle className="spin" aria-hidden="true" />
                      <span>{t(activeInspection.waiting ? 'browserWaiting' : 'inspectLoading')}</span>
                    </div>
                    <div className="link-preview-actions">
                      {activeInspection.waiting && (
                        <button type="button" className="secondary-button" disabled={busy} onClick={() => { void enqueueLink(activeInspection.url); }}>
                          {t('downloadDirectly')}
                        </button>
                      )}
                      <button type="button" className="secondary-button" onClick={() => {
                        dismissedUrl.current = activeInspection.url;
                        cancelInspect(true);
                      }}>{t('cancel')}</button>
                    </div>
                  </>
                ) : activeInspection.state === 'failed' ? (
                  <>
                    <p className="link-preview-error" role="alert">{failureText(preferences.language, activeInspection.error.code)}</p>
                    <div className="link-preview-actions">
                      {(activeInspection.error.code === 'telegram_network' || activeInspection.error.code === 'telegram_rate_limited') && (
                        <button type="button" className="secondary-button" disabled={busy} onClick={() => { void enqueueLink(activeInspection.url); }}>
                          {t('downloadDirectly')}
                        </button>
                      )}
                      <button type="button" className="secondary-button" disabled={busy} onClick={() => beginInspect(activeInspection.url)}>{t('refresh')}</button>
                      <button type="button" className="icon-button" aria-label={t('close')} onClick={() => {
                        dismissedUrl.current = activeInspection.url;
                        cancelInspect(true);
                      }}><X aria-hidden="true" /></button>
                    </div>
                  </>
                ) : (
                  <>
                    <div className="link-preview-media">
                      <div className="thumb" aria-hidden="true">
                        <MediaArtwork cover={imageSource(activeInspection.preview.thumbnail)} kind={activeInspection.preview.kind} />
                        {inspectedDuration && <span className="duration">{inspectedDuration}</span>}
                      </div>
                      <div className="link-preview-body">
                        <h2 title={inspectedTitle}>{inspectedTitle}</h2>
                        <div className="link-preview-meta">
                          <span>{inspectedMeta}</span>
                          {inspectedDuplicate && <><span aria-hidden="true">·</span><span className="link-preview-duplicate">{t('alreadyInDownloads')}</span></>}
                        </div>
                      </div>
                    </div>
                    <div className="link-preview-actions">
                      {activeInspection.preview.kind === 'video' && (
                        <button type="button" className="row-action" aria-haspopup="dialog" disabled={busy} onClick={() => openPreview({
                          url: activeInspection.url,
                          name: inspectedTitle,
                          thumbnail: activeInspection.preview.thumbnail,
                        })}>
                          <Play aria-hidden="true" /><span>{t('preview')}</span>
                        </button>
                      )}
                      <button type="button" className="primary-button" disabled={busy || !canConfirmPreview} onClick={() => { void enqueueLink(activeInspection.url); }}>
                        <ArrowDownToLine aria-hidden="true" /><span>{t('confirmDownload')}</span>
                      </button>
                      <button type="button" className="icon-button" aria-label={t('close')} onClick={() => {
                        dismissedUrl.current = activeInspection.url;
                        cancelInspect(true);
                      }}><X aria-hidden="true" /></button>
                    </div>
                  </>
                )}
              </div>
            )}
            {destination}
          </form>}
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
          {browsingAttachments
            ? account?.state === 'connected' && <ChannelAttachments
              key={`${view}:${account.backend ?? ''}:${account.label ?? ''}`} saved={view === 'saved'}
              t={t} language={preferences.language} busy={busy} destination={destination} notification={notification} onPreview={openPreview} onQueue={addBatch} />
            : <Downloads jobs={jobs} view={view} loading={!snapshot && !engineFailure} busy={busy} language={preferences.language} t={t} connected={account?.state === 'connected'} resumeRestarts={account?.backend === 'tdl'} onAction={onJobAction} onPlay={playDownload} />}
        </div>
      </main>
      {!browsingAttachments && <div className="notifications" role="status" aria-live="polite">
        {notification && <span>{notification}</span>}
      </div>}
      {settingsOpen && <SettingsDialog proxy={snapshot?.settings.proxy} t={t} language={preferences.language}
        onProxyChanged={async () => { await refresh(true); }} onClose={() => setSettingsOpen(false)} />}
      {preview && (preview.jobId || account?.state === 'connected') && !accountOpen && (
        <VideoPreview key={preview.jobId ?? preview.url} target={preview} t={t} language={preferences.language} onClose={() => setPreview(undefined)} onDownload={addFromPreview} />
      )}
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
