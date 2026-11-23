import { useEffect, useRef, useState } from 'react';
import type { SyntheticEvent } from 'react';
import { ArrowDownToLine, Film, X } from 'lucide-react';
import { failureOf, imageSource, request } from './api';
import type { Failure, TaskReference } from './api';
import { failureText } from './i18n';
import type { Language, Translate } from './i18n';
import { filesize } from './MediaArtwork';

interface PreviewArtwork {
  name?: string;
  thumbnail?: string;
}

export type TelegramPreviewTarget = PreviewArtwork & { url: string; jobId?: never };
export type PreviewTarget = TelegramPreviewTarget | (PreviewArtwork & { jobId: string; url?: never });

interface Props {
  target: PreviewTarget;
  language: Language;
  t: Translate;
  onClose: () => void;
  onDownload: (url: string) => Promise<Failure | undefined>;
}

interface PlaybackLoad {
  buffering: boolean;
  fetching: boolean;
  fraction?: number;
}

function bufferedFraction(video: HTMLVideoElement) {
  const { buffered, duration } = video;
  if (!Number.isFinite(duration) || duration <= 0 || buffered.length === 0) return;
  let seconds = 0;
  for (let index = 0; index < buffered.length; index++) {
    seconds += Math.max(0, Math.min(duration, buffered.end(index)) - Math.max(0, buffered.start(index)));
  }
  return Math.min(seconds / duration, 1);
}

export function VideoPreview({ target, language, t, onClose, onDownload }: Props) {
  const dialog = useRef<HTMLDialogElement>(null);
  const stop = useRef<(() => Promise<void>) | undefined>(undefined);
  const [stream, setStream] = useState<string>();
  const [waiting, setWaiting] = useState(false);
  const [issue, setIssue] = useState<Failure>();
  const [playbackError, setPlaybackError] = useState(false);
  const [downloading, setDownloading] = useState(false);
  const [attempt, setAttempt] = useState(0);
  const [playback, setPlayback] = useState<PlaybackLoad>({ buffering: true, fetching: false });
  const [speed, setSpeed] = useState<number>();
  const local = target.jobId !== undefined;
  const heading = t(local ? 'playVideo' : 'previewTitle');

  useEffect(() => {
    const element = dialog.current;
    element?.showModal();
    return () => { if (element?.open) element.close(); };
  }, []);

  useEffect(() => {
    let stopped = false;
    let timer: number | undefined;
    let cancellation: Promise<void> | undefined;
    setIssue(undefined);
    setPlaybackError(false);
    setStream(undefined);
    setWaiting(false);
    setPlayback({ buffering: true, fetching: false });
    setSpeed(undefined);
    const started = target.jobId === undefined
      ? request('preview_start', { url: target.url })
      : request('local_preview_start', { id: target.jobId });

    function cancel() {
      stopped = true;
      window.clearTimeout(timer);
      cancellation ??= started.then(ref => request('preview_stop', ref)).then(() => undefined, () => undefined);
      return cancellation;
    }

    function fail(error: unknown) {
      if (stopped) return;
      setStream(undefined);
      setIssue(failureOf(error, 'preview_unavailable'));
      void cancel();
    }

    async function poll(ref: TaskReference) {
      if (stopped) return;
      try {
        const status = await request('preview_status', ref);
        if (stopped) return;
        if (status.state === 'failed') throw status.error ?? { code: 'preview_unavailable' };
        if (status.state === 'loading') {
          setWaiting(Boolean(status.waiting));
        } else {
          if (!/^http:\/\/127\.0\.0\.1:\d{1,5}\/preview\/[a-f\d]{64}$/.test(status.url)) {
            throw { code: 'preview_unavailable' };
          }
          setStream(status.url);
          setSpeed(status.speed !== undefined && Number.isFinite(status.speed) && status.speed >= 0 ? status.speed : undefined);
        }
        timer = window.setTimeout(() => { void poll(ref); }, status.state === 'ready' ? 650 : 420);
      } catch (error) {
        fail(error);
      }
    }

    stop.current = cancel;
    void started.then(poll, fail);

    return () => {
      stop.current = undefined;
      void cancel();
    };
  }, [target.jobId, target.url, attempt]);

  function updatePlayback(event: SyntheticEvent<HTMLVideoElement>, waitingForData = false) {
    const video = event.currentTarget;
    setPlayback({
      buffering: !video.ended && (waitingForData || video.seeking || video.readyState < HTMLMediaElement.HAVE_METADATA ||
        (!video.paused && video.readyState < HTMLMediaElement.HAVE_FUTURE_DATA)),
      fetching: !video.ended && video.networkState === HTMLMediaElement.NETWORK_LOADING,
      fraction: bufferedFraction(video),
    });
  }

  function failPlayback() {
    setStream(undefined);
    setPlaybackError(true);
    void stop.current?.();
  }

  async function download() {
    if (downloading || target.url === undefined) return;
    setDownloading(true);
    setStream(undefined);
    await stop.current?.();
    const failure = await onDownload(target.url);
    if (!failure) onClose();
    else {
      setDownloading(false);
      setPlaybackError(false);
      setIssue(failure);
    }
  }

  const showTransfer = stream && (playback.buffering || playback.fetching || (!local && (speed ?? 0) > 0));

  return (
    <dialog className="account-dialog video-preview" ref={dialog} aria-labelledby="video-preview-title" onCancel={event => { event.preventDefault(); if (!downloading) onClose(); }}>
      <div className="dialog-head">
        <div>
          <h2 id="video-preview-title">{heading}</h2>
          {target.name && <p className="video-name" title={target.name}>{target.name}</p>}
        </div>
        <button className="icon-button" type="button" aria-label={t('close')} disabled={downloading} onClick={onClose}><X aria-hidden="true" /></button>
      </div>
      <div className="video-stage">
        {stream ? (
          <video aria-label={target.name || heading} src={stream} poster={imageSource(target.thumbnail)}
            controls autoPlay={local} playsInline preload="metadata" onError={failPlayback}
            onLoadStart={updatePlayback} onProgress={updatePlayback} onDurationChange={updatePlayback}
            onSeeking={updatePlayback} onSeeked={updatePlayback} onCanPlay={updatePlayback}
            onPlay={updatePlayback} onPlaying={updatePlayback} onPause={updatePlayback} onEnded={updatePlayback}
            onWaiting={event => updatePlayback(event, true)} onStalled={updatePlayback} onSuspend={updatePlayback} />
        ) : (
          <div className="video-state">
            <Film aria-hidden="true" />
            {issue || playbackError ? (
              <p role="alert">{playbackError ? t(local ? 'playPlaybackError' : 'previewPlaybackError') : failureText(language, issue?.code ?? 'preview_unavailable')}</p>
            ) : (
              <p role="status">{t(downloading ? 'queued' : waiting ? 'browserWaiting' : local ? 'playLoading' : 'previewLoading')}</p>
            )}
          </div>
        )}
      </div>
      {(showTransfer || issue || playbackError || !local) && <div className="dialog-actions">
        {showTransfer && <div className="preview-transfer">
          <div className="preview-transfer-numbers">
            <span role="status">
              {t(playback.buffering ? 'previewBuffering' : local ? 'playLoading' : 'previewFetching')}
              {playback.fraction !== undefined && ` ${Math.floor(playback.fraction * 100)}%`}
            </span>
            {!local && <span>{t('previewSpeed')} <b>{speed === undefined ? '—' : `${filesize(speed)}/s`}</b></span>}
          </div>
          <progress aria-label={t('previewBufferedProgress')} max={1} value={playback.fraction} />
        </div>}
        {(issue || playbackError) && <button className="secondary-button" type="button" disabled={downloading} onClick={() => setAttempt(value => value + 1)}>{t('refresh')}</button>}
        {!local && <button className="primary-button" type="button" disabled={downloading} onClick={() => { void download(); }}><ArrowDownToLine aria-hidden="true" />{t('download')}</button>}
      </div>}
    </dialog>
  );
}
