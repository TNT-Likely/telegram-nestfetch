import { useEffect, useRef, useState } from 'react';
import { ArrowDown, CircleCheck, Ellipsis, Eye, EyeOff, FolderOpen, HardDriveDownload, Pause, Play, RotateCcw, X } from 'lucide-react';
import { imageSource, request } from './api';
import type { Failure, Job } from './api';
import { failureText } from './i18n';
import type { Language, Translate } from './i18n';
import { duration, filesize, MediaArtwork } from './MediaArtwork';

export type View = 'all' | 'active' | 'completed';
export type JobAction = 'pause' | 'resume' | 'retry' | 'save_elsewhere' | 'reveal' | 'hide_job' | 'restore_job' | 'remove_job';

export interface JobActionOptions {
  discardStaged?: boolean;
  onError?: (failure: Failure | undefined) => void;
}

const transferable = new Set<Job['state']>(['resolving', 'downloading', 'saving']);
const canPause = new Set<Job['state']>(['queued', 'resolving', 'downloading']);
const savedMessagePrefix = 'nestfetch://saved/';

export function countByView(jobs: Job[], view: View) {
  return jobs.filter(job => inView(job, view)).length;
}

function inView(job: Job, view: View, showHidden = false) {
  if (job.hidden && !(view === 'completed' && showHidden)) return false;
  return view === 'all' || (view === 'completed' ? job.state === 'completed' : job.state !== 'completed');
}

function useCover(id: string, hasThumbnail: boolean) {
  const element = useRef<HTMLDivElement>(null);
  const [loaded, setLoaded] = useState<{ id: string; source: string }>();
  useEffect(() => {
    if (!hasThumbnail) {
      setLoaded(undefined);
      return;
    }
    if (!element.current) return;
    let disposed = false;
    let attempts = 0;
    let timer: number | undefined;
    async function load() {
      attempts++;
      try {
        const source = imageSource(await request('thumbnail', { id }));
        if (disposed) return;
        if (source) {
          setLoaded({ id, source });
          return;
        }
      } catch {
        if (disposed) return;
      }
      if (attempts < 3) timer = window.setTimeout(load, 1500 * attempts);
    }
    const observer = new IntersectionObserver(entries => {
      if (entries.some(entry => entry.isIntersecting)) {
        observer.disconnect();
        void load();
      }
    });
    observer.observe(element.current);
    return () => {
      disposed = true;
      observer.disconnect();
      window.clearTimeout(timer);
    };
  }, [id, hasThumbnail]);
  return { element, cover: hasThumbnail && loaded?.id === id ? loaded.source : undefined };
}

interface JobProps {
  job: Job;
  t: Translate;
  language: Language;
  connected: boolean;
  resumeRestarts: boolean;
  busy: boolean;
  onAction: (job: Job, action: JobAction, options?: JobActionOptions) => Promise<boolean>;
  onPlay: (job: Job, cover: string | undefined) => void;
  onDetails: (id: string) => void;
}

function jobTitle(job: Job, t: Translate) {
  return job.title || job.name || t('telegramAttachment');
}

function jobSource(job: Job, t: Translate) {
  return job.url.startsWith(savedMessagePrefix) ? t('savedMessages') : job.source;
}

function Status({ job, t }: Pick<JobProps, 'job' | 't'>) {
  return <span className="job-state"><i className="dot" />{t(job.state)}</span>;
}

function TransferProgress({ job, t, resumeRestarts }: Pick<JobProps, 'job' | 't' | 'resumeRestarts'>) {
  if (job.state === 'saving') return <p className="transfer-info dialog-copy">{t('savingDescription')}</p>;
  if (job.transferDone) return <p className="transfer-info dialog-copy">{t('stagedDescription')}</p>;
  const known = job.size > 0;
  const transferred = known ? Math.min(job.size, Math.max(0, job.downloaded)) : Math.max(0, job.downloaded);
  return (
    <div className="transfer-info">
      <div className="transfer-numbers">
        {known && <strong>{Math.floor(transferred / job.size * 100)}%</strong>}
        <span>{filesize(transferred)}{known && ' / ' + filesize(job.size)}</span>
        {job.state === 'downloading' && job.speed > 0 && (
          <span className="speed"><ArrowDown aria-hidden="true" />{filesize(job.speed)}/s</span>
        )}
      </div>
      <progress
        aria-label={t('progress')}
        max={known ? job.size : 1}
        value={known ? transferred : undefined}
      />
      {job.state === 'paused' && resumeRestarts && <p className="row-hint">{t('resumeRestarts')}</p>}
    </div>
  );
}

function DetailsButton({ job, t, busy, onDetails, className }: Pick<JobProps, 'job' | 't' | 'busy' | 'onDetails'> & { className: string }) {
  return (
    <button className={className} type="button" title={t('downloadDetails')} aria-label={`${t('downloadDetails')} · ${jobTitle(job, t)}`}
      aria-haspopup="dialog" disabled={busy} onClick={() => onDetails(job.id)}>
      <Ellipsis aria-hidden="true" />
    </button>
  );
}

function FeaturedJob({ job, t, resumeRestarts, busy, onAction, onDetails }: JobProps) {
  const { element, cover } = useCover(job.id, job.hasThumbnail);
  const seconds = duration(job.duration);
  const title = jobTitle(job, t);
  const source = jobSource(job, t);
  const action = job.state === 'paused' ? 'resume' : canPause.has(job.state) ? 'pause' : null;
  const ActionIcon = action === 'resume' ? Play : Pause;
  return (
    <article className="feature" data-state={job.state} aria-labelledby="featured-title">
      <div className="feature-still" ref={element}>
        <MediaArtwork cover={cover} kind={job.kind} />
        <div className="feature-top">
          <Status job={job} t={t} />
          {seconds && <span className="duration">{seconds}</span>}
        </div>
        <div className="feature-caption">
          <div className="feature-label">
            <h2 id="featured-title" title={title}>{title}</h2>
            {source && <span title={source}>{source}</span>}
          </div>
          <div className="feature-actions">
            {action && (
              <button type="button" className="transfer-toggle" aria-label={t(action)} title={t(action)}
                disabled={busy} onClick={() => { void onAction(job, action); }}>
                <ActionIcon aria-hidden="true" />
              </button>
            )}
            <DetailsButton job={job} t={t} busy={busy} onDetails={onDetails} className="transfer-toggle" />
          </div>
        </div>
      </div>
      <TransferProgress job={job} t={t} resumeRestarts={resumeRestarts} />
    </article>
  );
}

function DownloadRow({ job, t, language, connected, resumeRestarts, busy, onAction, onPlay, onDetails }: JobProps) {
  const { element, cover } = useCover(job.id, job.hasThumbnail);
  const seconds = duration(job.duration);
  const title = jobTitle(job, t);
  const action = job.state === 'waiting_save' || job.state === 'failed' ? 'retry'
    : job.state === 'completed' ? 'reveal'
      : job.state === 'paused' ? 'resume'
        : canPause.has(job.state) ? 'pause' : null;
  const caption = job.state === 'waiting_save' ? t('retrySave') : action ? t(action) : '';
  const ActionIcon = action === 'retry' ? RotateCcw : action === 'reveal' ? FolderOpen : action === 'resume' ? Play : Pause;
  const detail = job.state === 'waiting_save' ? t('waitingDescription')
    : job.error ? failureText(language, job.error.code)
      : job.transferDone && job.state !== 'completed' && job.state !== 'removing' ? t('stagedDescription')
        : !connected && job.state === 'queued' ? t('waitingForLogin')
          : job.state === 'paused' && resumeRestarts ? t('resumeRestarts')
            : job.size > 0 ? filesize(job.size) : jobSource(job, t);
  return (
    <li className="download-row" data-state={job.state}>
      <div className="thumb" ref={element} aria-hidden="true">
        <MediaArtwork cover={cover} kind={job.kind} />
        {seconds && <span className="duration">{seconds}</span>}
      </div>
      <div className="row-body">
        <h3 title={title}>{title}</h3>
        <div className="row-meta">
          <Status job={job} t={t} />
          {job.hidden && <span className="hidden-status"><EyeOff aria-hidden="true" />{t('hiddenDownload')}</span>}
          {detail && <><span aria-hidden="true">·</span><span className="row-detail">{detail}</span></>}
        </div>
        {job.state === 'waiting_save' && job.error && (
          <div className="row-hint">{failureText(language, job.error.code)}</div>
        )}
        {transferable.has(job.state) && job.size > 0 && (
          <progress aria-label={t('progress')} value={Math.max(0, job.downloaded)} max={job.size} />
        )}
      </div>
      <div className="row-actions">
        {job.state === 'completed' && job.kind === 'video' && (
          <button className="row-action" type="button" title={t('play')} aria-label={`${t('play')} · ${title}`} aria-haspopup="dialog" disabled={busy} onClick={() => onPlay(job, cover)}>
            <Play aria-hidden="true" />
            <span>{t('play')}</span>
          </button>
        )}
        {job.state === 'waiting_save' && (
          <button className="row-action" type="button" title={t('saveElsewhere')} aria-label={t('saveElsewhere') + ' · ' + title} disabled={busy} onClick={() => onAction(job, 'save_elsewhere')}>
            <FolderOpen aria-hidden="true" />
            <span>{t('saveElsewhere')}</span>
          </button>
        )}
        {action && (
          <button className="row-action" type="button" title={caption} aria-label={caption + ' · ' + title} disabled={busy} onClick={() => onAction(job, action)}>
            <ActionIcon aria-hidden="true" />
            <span>{caption}</span>
          </button>
        )}
        <DetailsButton job={job} t={t} busy={busy} onDetails={onDetails} className="row-action" />
      </div>
    </li>
  );
}

interface DownloadDetailsProps extends Omit<JobProps, 'onPlay' | 'onDetails'> {
  onClose: () => void;
}

function DownloadDetails({ job, t, language, busy, connected, resumeRestarts, onAction, onClose }: DownloadDetailsProps) {
  const dialog = useRef<HTMLDialogElement>(null);
  const [confirming, setConfirming] = useState(false);
  const [actionFailure, setActionFailure] = useState<Failure>();
  useEffect(() => {
    const current = dialog.current;
    current?.showModal();
    return () => { if (current?.open) current.close(); };
  }, []);

  const removal = job.state === 'saving' || job.state === 'completed' ? undefined
    : job.state === 'removing' ? 'retryRemoval'
      : canPause.has(job.state) ? 'cancelDownload' : 'removeDownload';
  const staged = job.transferDone || job.state === 'waiting_save';
  const confirmRemoval = confirming && removal && job.state !== 'removing';
  const failure = actionFailure ?? job.error;
  const showProgress = job.state === 'resolving' || job.state === 'downloading' || (job.state === 'paused' && !staged);
  const stage = job.state === 'saving' ? t('savingDescription')
    : staged && job.state !== 'completed' && job.state !== 'removing' ? t('stagedDescription')
      : job.state === 'queued' && !connected ? t('waitingForLogin') : undefined;
  const title = jobTitle(job, t);
  const fromSavedMessages = job.url.startsWith(savedMessagePrefix);
  const VisibleIcon = job.hidden ? Eye : EyeOff;

  async function perform(action: JobAction, discardStaged = false) {
    const succeeded = await onAction(job, action, { discardStaged, onError: setActionFailure });
    if (succeeded && (action === 'hide_job' || action === 'restore_job' || action === 'remove_job')) onClose();
  }

  function closeConfirmation() {
    setConfirming(false);
    setActionFailure(undefined);
  }

  return (
    <dialog ref={dialog} className="account-dialog download-details" aria-labelledby="download-details-title" onCancel={event => {
      event.preventDefault();
      if (!busy) confirmRemoval ? closeConfirmation() : onClose();
    }}>
      <div className="dialog-head">
        <h2 id="download-details-title">{t(confirmRemoval ? 'discardStagedTitle' : 'downloadDetails')}</h2>
        <button className="icon-button" type="button" aria-label={t('close')} disabled={busy} onClick={onClose}><X aria-hidden="true" /></button>
      </div>
      <h3 className="details-file-title">{title}</h3>
      {confirmRemoval ? (
        <>
          <p className="dialog-copy details-stage">{t('discardStagedDescription')}</p>
          {actionFailure && <p className="dialog-error" role="alert">{failureText(language, actionFailure.code)}</p>}
          <div className="dialog-actions dialog-actions-wrap">
            <button type="button" className="secondary-button" autoFocus disabled={busy} onClick={closeConfirmation}>{t('cancel')}</button>
            <button type="button" className="primary-button" disabled={busy} onClick={() => { void perform('remove_job', true); }}>{t('discardStaged')}</button>
          </div>
        </>
      ) : (
        <>
          <div className="row-meta details-status">
            <Status job={job} t={t} />
            {job.hidden && <span className="hidden-status"><EyeOff aria-hidden="true" />{t('hiddenDownload')}</span>}
          </div>
          {stage && <p className="dialog-copy details-stage">{stage}</p>}
          {showProgress && <TransferProgress job={job} t={t} resumeRestarts={resumeRestarts} />}
          {failure && <p className="dialog-error" role="alert">{failureText(language, failure.code)}</p>}
          <dl className="details-fields">
            <div>
              <dt>{t(fromSavedMessages ? 'downloadSource' : 'link')}</dt>
              <dd>{fromSavedMessages ? t('savedMessages') : job.url}</dd>
            </div>
            <div><dt>{t(job.outputPath ? 'savedPath' : 'saveTo')}</dt><dd>{job.outputPath || job.folder}</dd></div>
            {job.size > 0 && !showProgress && <div><dt>{t('fileSize')}</dt><dd>{filesize(job.size)}</dd></div>}
          </dl>
          <div className="dialog-actions dialog-actions-wrap">
            {job.state === 'waiting_save' && <>
              <button className="secondary-button" type="button" disabled={busy} onClick={() => { void perform('save_elsewhere'); }}>{t('saveElsewhere')}</button>
              <button className="secondary-button" type="button" disabled={busy} onClick={() => { void perform('retry'); }}>{t('retrySave')}</button>
            </>}
            {job.state === 'completed' && <>
              <button className="secondary-button" type="button" disabled={busy} onClick={() => { void perform('reveal'); }}>{t('reveal')}</button>
              <button className="secondary-button" type="button" disabled={busy} onClick={() => { void perform(job.hidden ? 'restore_job' : 'hide_job'); }}>
                <VisibleIcon aria-hidden="true" />{t(job.hidden ? 'restoreDownload' : 'hideDownload')}
              </button>
            </>}
            {removal && <button className="secondary-button job-remove-button" type="button" disabled={busy} onClick={() => {
              if (staged && job.state !== 'removing') { setActionFailure(undefined); setConfirming(true); }
              else void perform('remove_job');
            }}>{t(removal)}</button>}
          </div>
        </>
      )}
    </dialog>
  );
}

interface DownloadsProps extends Omit<JobProps, 'job' | 'onDetails'> {
  jobs: Job[];
  view: View;
  loading: boolean;
}

export function Downloads({ jobs, view, loading, ...props }: DownloadsProps) {
  const { t } = props;
  const [showHidden, setShowHidden] = useState(false);
  const [detailsId, setDetailsId] = useState<string>();
  const visible = jobs.filter(job => inView(job, view, showHidden));
  const details = jobs.find(job => job.id === detailsId);
  const rowProps = { ...props, onDetails: setDetailsId };
  const featured = visible.find(job => transferable.has(job.state))
    ?? visible.find(job => job.state === 'paused' && job.size > 0);
  const rows = visible.filter(job => job !== featured);
  const emptyTitle = loading ? t('loading') : view === 'completed' ? t('emptyCompleted') : view === 'active' ? t('emptyActive') : t('empty');
  return (
    <section className="downloads" aria-label={t('queue')} aria-busy={loading}>
      {view === 'completed' && jobs.some(job => job.hidden) && (
        <div className="downloads-toolbar">
          <label><input type="checkbox" checked={showHidden} onChange={event => setShowHidden(event.currentTarget.checked)} />{t('showHidden')}</label>
        </div>
      )}
      {featured && <FeaturedJob key={featured.id} job={featured} {...rowProps} />}
      {rows.length > 0 && (
        <ul className="download-list">
          {rows.map(job => <DownloadRow key={job.id} job={job} {...rowProps} />)}
        </ul>
      )}
      {details && <DownloadDetails key={details.id} job={details} {...props} onClose={() => setDetailsId(undefined)} />}
      {visible.length === 0 && (
        <div className="empty">
          <span className="empty-icon">{view === 'completed' ? <CircleCheck aria-hidden="true" /> : <HardDriveDownload aria-hidden="true" />}</span>
          <h2>{emptyTitle}</h2>
          {!loading && view === 'all' && <p>{t('emptyDescription')}</p>}
        </div>
      )}
    </section>
  );
}
