import { useEffect, useRef, useState } from 'react';
import { ArrowDown, CircleCheck, Film, FolderOpen, HardDriveDownload, Pause, Play, RotateCcw } from 'lucide-react';
import { imageSource, request } from './api';
import type { Job } from './api';
import { failureText } from './i18n';
import type { Language, Translate } from './i18n';

export type View = 'all' | 'active' | 'completed';
export type JobAction = 'pause' | 'resume' | 'retry' | 'save_elsewhere' | 'reveal';

const transferable = new Set<Job['state']>(['resolving', 'downloading', 'saving']);
const canPause = new Set<Job['state']>(['queued', 'resolving', 'downloading']);

export function countByView(jobs: Job[], view: View) {
  return jobs.filter(job => inView(job, view)).length;
}

function inView(job: Job, view: View) {
  return view === 'all' || (view === 'completed' ? job.state === 'completed' : job.state !== 'completed');
}

function filesize(bytes: number) {
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  if (!Number.isFinite(bytes)) return '—';
  let size = Math.max(0, bytes);
  let index = 0;
  while (size >= 1000 && index < units.length - 1) {
    size /= 1000;
    index++;
  }
  return size.toFixed(index ? 1 : 0) + ' ' + units[index];
}

function duration(seconds: number) {
  if (!Number.isFinite(seconds) || seconds <= 0) return null;
  const whole = Math.floor(seconds);
  const values: number[] = [Math.floor(whole / 60) % 60, whole % 60];
  if (whole >= 3600) values.unshift(Math.floor(whole / 3600));
  return values.map(value => String(value).padStart(2, '0')).join(':');
}

function useCover(id: string, hasThumbnail: boolean) {
  const element = useRef<HTMLDivElement>(null);
  const [cover, setCover] = useState<string>();
  useEffect(() => {
    if (!hasThumbnail || !element.current) return;
    let disposed = false;
    let attempts = 0;
    let timer: number | undefined;
    async function load() {
      attempts++;
      try {
        const source = imageSource(await request('thumbnail', { id }));
        if (disposed) return;
        if (source) {
          setCover(source);
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
  return { element, cover };
}

function MediaImage({ cover }: { cover?: string }) {
  const [failed, setFailed] = useState<string>();
  return cover && failed !== cover
    ? <img alt="" draggable={false} src={cover} onError={() => setFailed(cover)} />
    : <Film className="film-placeholder" aria-hidden="true" />;
}

interface JobProps {
  job: Job;
  t: Translate;
  language: Language;
  connected: boolean;
  resumeRestarts: boolean;
  busy: boolean;
  onAction: (job: Job, action: JobAction) => void;
}

function jobTitle(job: Job, t: Translate) {
  return job.title || job.name || t('telegramVideo');
}

function Status({ job, t }: Pick<JobProps, 'job' | 't'>) {
  return <span className="job-state"><i className="dot" />{t(job.state)}</span>;
}

function TransferProgress({ job, t, resumeRestarts }: Pick<JobProps, 'job' | 't' | 'resumeRestarts'>) {
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

function FeaturedJob({ job, t, resumeRestarts, busy, onAction }: JobProps) {
  const { element, cover } = useCover(job.id, job.hasThumbnail);
  const seconds = duration(job.duration);
  const title = jobTitle(job, t);
  const action = job.state === 'paused' ? 'resume' : canPause.has(job.state) ? 'pause' : null;
  const ActionIcon = action === 'resume' ? Play : Pause;
  return (
    <article className="feature" data-state={job.state} aria-labelledby="featured-title">
      <div className="feature-still" ref={element}>
        <MediaImage cover={cover} />
        <div className="feature-top">
          <Status job={job} t={t} />
          {seconds && <span className="duration">{seconds}</span>}
        </div>
        <div className="feature-caption">
          <div className="feature-label">
            <h2 id="featured-title" title={title}>{title}</h2>
            {job.source && <span title={job.source}>{job.source}</span>}
          </div>
          {action && (
            <button
              type="button"
              className="transfer-toggle"
              aria-label={t(action)}
              title={t(action)}
              disabled={busy}
              onClick={() => onAction(job, action)}
            >
              <ActionIcon aria-hidden="true" />
            </button>
          )}
        </div>
      </div>
      <TransferProgress job={job} t={t} resumeRestarts={resumeRestarts} />
    </article>
  );
}

function DownloadRow({ job, t, language, connected, resumeRestarts, busy, onAction }: JobProps) {
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
      : !connected && job.state === 'queued' ? t('waitingForLogin')
        : job.state === 'paused' && resumeRestarts ? t('resumeRestarts')
          : job.size > 0 ? filesize(job.size) : job.source;
  return (
    <li className="download-row" data-state={job.state}>
      <div className="thumb" ref={element} aria-hidden="true">
        <MediaImage cover={cover} />
        {seconds && <span className="duration">{seconds}</span>}
      </div>
      <div className="row-body">
        <h3 title={title}>{title}</h3>
        <div className="row-meta">
          <Status job={job} t={t} />
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
      </div>
    </li>
  );
}

interface DownloadsProps extends Omit<JobProps, 'job'> {
  jobs: Job[];
  view: View;
  loading: boolean;
}

export function Downloads({ jobs, view, loading, ...props }: DownloadsProps) {
  const { t } = props;
  const visible = jobs.filter(job => inView(job, view));
  const featured = visible.find(job => transferable.has(job.state))
    ?? visible.find(job => job.state === 'paused' && job.size > 0);
  const rows = visible.filter(job => job !== featured);
  const emptyTitle = loading ? t('loading') : view === 'completed' ? t('emptyCompleted') : view === 'active' ? t('emptyActive') : t('empty');
  return (
    <section className="downloads" aria-label={t('queue')} aria-busy={loading}>
      {featured && <FeaturedJob key={featured.id} job={featured} {...props} />}
      {rows.length > 0 && (
        <ul className="download-list">
          {rows.map(job => <DownloadRow key={job.id} job={job} {...props} />)}
        </ul>
      )}
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
