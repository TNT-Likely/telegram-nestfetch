import { useEffect, useRef, useState } from 'react';
import { ArrowDownToLine, Film, X } from 'lucide-react';
import { failureOf, imageSource, request, startTask } from './api';
import type { Failure } from './api';
import { failureText } from './i18n';
import type { Language, Translate } from './i18n';

export interface PreviewTarget {
  url: string;
  name?: string;
  thumbnail?: string;
}

interface Props {
  target: PreviewTarget;
  language: Language;
  t: Translate;
  onClose: () => void;
  onDownload: (url: string) => Promise<Failure | undefined>;
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

  useEffect(() => {
    const element = dialog.current;
    element?.showModal();
    return () => { if (element?.open) element.close(); };
  }, []);

  useEffect(() => {
    let live = true;
    setIssue(undefined);
    setPlaybackError(false);
    setStream(undefined);
    setWaiting(false);
    const task = startTask(
      () => request('preview_start', { url: target.url }),
      id => request('preview_status', id),
      id => request('preview_stop', id),
      setWaiting,
    );
    stop.current = task.stop;
    void task.result.then(status => {
      if (!live || !status) return;
      if (!/^http:\/\/127\.0\.0\.1:\d{1,5}\/preview\/[a-f\d]{64}$/.test(status.url)) {
        throw { code: 'preview_unavailable' };
      }
      setStream(status.url);
    }).catch(error => {
      if (!live) return;
      setIssue(failureOf(error, 'preview_unavailable'));
      void task.stop();
    });

    return () => {
      live = false;
      stop.current = undefined;
      void task.stop();
    };
  }, [target.url, attempt]);

  function failPlayback() {
    setStream(undefined);
    setPlaybackError(true);
    void stop.current?.();
  }

  async function download() {
    if (downloading) return;
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

  return (
    <dialog className="account-dialog video-preview" ref={dialog} aria-labelledby="video-preview-title" onCancel={event => { event.preventDefault(); if (!downloading) onClose(); }}>
      <div className="dialog-head">
        <div>
          <h2 id="video-preview-title">{t('previewTitle')}</h2>
          {target.name && <p className="video-name" title={target.name}>{target.name}</p>}
        </div>
        <button className="icon-button" type="button" aria-label={t('close')} disabled={downloading} onClick={onClose}><X aria-hidden="true" /></button>
      </div>
      <div className="video-stage">
        {stream ? (
          <video aria-label={target.name || t('previewTitle')} src={stream} poster={imageSource(target.thumbnail)} controls playsInline preload="metadata" onError={failPlayback} />
        ) : (
          <div className="video-state">
            <Film aria-hidden="true" />
            {issue || playbackError ? (
              <p role="alert">{playbackError ? t('previewPlaybackError') : failureText(language, issue?.code ?? 'preview_unavailable')}</p>
            ) : (
              <p role="status">{t(downloading ? 'queued' : waiting ? 'browserWaiting' : 'previewLoading')}</p>
            )}
          </div>
        )}
      </div>
      <div className="dialog-actions">
        {(issue || playbackError) && <button className="secondary-button" type="button" disabled={downloading} onClick={() => setAttempt(value => value + 1)}>{t('refresh')}</button>}
        <button className="primary-button" type="button" disabled={downloading} onClick={() => { void download(); }}><ArrowDownToLine aria-hidden="true" />{t('download')}</button>
      </div>
    </dialog>
  );
}
