import { useCallback, useEffect, useRef, useState } from 'react';
import type { FormEvent, ReactNode } from 'react';
import { ArrowDownToLine, ChevronDown, Play, RotateCw, Search } from 'lucide-react';
import { failureOf, imageSource, request, startTask } from './api';
import type { Attachment, AttachmentQuery, Channel, Failure, MediaKind } from './api';
import { failureText } from './i18n';
import type { Language, Translate } from './i18n';
import { duration, filesize, MediaArtwork } from './MediaArtwork';

const batchLimit = 60;
const categories = ['video', 'image', 'audio', 'file'] as const;
type Category = 'all' | MediaKind;
type LoadState = 'loading' | 'waiting';
type Stop = () => Promise<void>;

interface Props {
  saved: boolean;
  t: Translate;
  language: Language;
  busy: boolean;
  destination: ReactNode;
  notification?: string;
  onPreview: (attachment: Attachment) => void;
  onQueue: (urls: string[]) => Promise<boolean>;
}

interface Results {
  query: AttachmentQuery;
  items: Attachment[];
  nextBefore?: number;
}

function dayInput(offset = 0) {
  const day = new Date();
  day.setDate(day.getDate() + offset);
  return [day.getFullYear(), String(day.getMonth() + 1).padStart(2, '0'), String(day.getDate()).padStart(2, '0')].join('-');
}

function daySeconds(value: string, end = false) {
  if (!/^\d{4}-\d{2}-\d{2}$/.test(value)) return NaN;
  const [year, month, date] = value.split('-').map(Number);
  const day = new Date(year, month - 1, date + (end ? 1 : 0));
  return day.getTime() / 1000 - Number(end);
}

function savedMessagesQuery(): AttachmentQuery {
  // Telegram history offsets use signed 32-bit Unix timestamps.
  const to = Math.min(daySeconds(dayInput(), true), 2 ** 31 - 2);
  return { saved: true, channel: '', from: 1, to };
}

export function ChannelAttachments({ saved, t, language, busy, destination, notification, onPreview, onQueue }: Props) {
  const [channels, setChannels] = useState<Channel[]>();
  const [channelsState, setChannelsState] = useState<LoadState | 'failed'>();
  const [source, setSource] = useState('');
  const [channelLink, setChannelLink] = useState('');
  const [fromDay, setFromDay] = useState(() => dayInput(-29));
  const [toDay, setToDay] = useState(dayInput);
  const [category, setCategory] = useState<Category>('all');
  const [results, setResults] = useState<Results>();
  const [pageState, setPageState] = useState<LoadState>();
  const [error, setError] = useState<Failure>();
  const [selected, setSelected] = useState<Set<string>>(() => new Set());
  const stopChannels = useRef<Stop | undefined>(undefined);
  const stopPage = useRef<Stop | undefined>(undefined);
  const selectVisible = useRef<HTMLInputElement>(null);
  const manual = source === 'manual' || channels?.length === 0 || (channelsState === 'failed' && !source);
  const today = dayInput();
  const visible = results?.items.filter(item => category === 'all' || item.kind === category) ?? [];
  const visibleSelected = visible.filter(item => selected.has(item.url)).length;
  const allVisibleSelected = visible.length > 0 && visibleSelected === visible.length;
  const date = new Intl.DateTimeFormat(language === 'zh' ? 'zh-CN' : 'en', { dateStyle: 'medium' });

  useEffect(() => {
    if (selectVisible.current) selectVisible.current.indeterminate = visibleSelected > 0 && !allVisibleSelected;
  }, [visibleSelected, allVisibleSelected]);

  function cancelPage() {
    const stop = stopPage.current;
    stopPage.current = undefined;
    void stop?.();
    setPageState(undefined);
  }

  function editQuery() {
    cancelPage();
    setResults(undefined);
    setSelected(new Set());
    setError(undefined);
  }

  const readPage = useCallback((query: AttachmentQuery, before?: number) => {
    if (stopPage.current) return;
    const stop = stopChannels.current;
    if (stop) {
      stopChannels.current = undefined;
      void stop();
      setChannelsState(undefined);
    }
    setPageState('loading');
    setError(undefined);
    const task = startTask(
      () => request('attachments_start', { ...query, before }),
      id => request('attachments_status', id),
      id => request('browse_cancel', id),
      waiting => setPageState(waiting ? 'waiting' : 'loading'),
    );
    stopPage.current = task.stop;
    void task.result.then(status => {
      if (stopPage.current !== task.stop || !status) return;
      setResults(previous => {
        const items = new Map((before ? previous?.items : [])?.map(entry => [entry.url, entry]));
        for (const item of status.items ?? []) items.set(item.url, item);
        const next = status.nextBefore;
        return {
          query,
          items: [...items.values()],
          nextBefore: next && (!before || next < before) ? next : undefined,
        };
      });
    }).catch(failure => {
      if (stopPage.current === task.stop) setError(failureOf(failure));
    }).finally(() => {
      if (stopPage.current === task.stop) {
        stopPage.current = undefined;
        setPageState(undefined);
      }
      void task.stop();
    });
  }, []);

  useEffect(() => {
    if (saved) {
      readPage(savedMessagesQuery());
    } else {
      setChannelsState('loading');
      const task = startTask(
        () => request('channels_start', undefined),
        id => request('channels_status', id),
        id => request('browse_cancel', id),
        waiting => setChannelsState(waiting ? 'waiting' : 'loading'),
      );
      stopChannels.current = task.stop;
      void task.result.then(status => {
        if (stopChannels.current !== task.stop || !status) return;
        setChannels(status.channels ?? []);
        setChannelsState(undefined);
      }).catch(() => {
        if (stopChannels.current === task.stop) setChannelsState('failed');
      }).finally(() => {
        if (stopChannels.current === task.stop) stopChannels.current = undefined;
        void task.stop();
      });
    }
    return () => {
      const stopList = stopChannels.current;
      const stopAttachments = stopPage.current;
      stopChannels.current = undefined;
      stopPage.current = undefined;
      void stopList?.();
      void stopAttachments?.();
    };
  }, [readPage, saved]);

  function refreshSavedMessages() {
    editQuery();
    readPage(savedMessagesQuery());
  }

  function browse(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const channel = (manual ? channelLink : source).trim();
    const from = daySeconds(fromDay);
    const to = daySeconds(toDay, true);
    if (!channel || !Number.isFinite(from) || !Number.isFinite(to) || to < from || toDay > today) {
      setError({ code: !channel ? 'invalid_channel' : 'invalid_date_range' });
      return;
    }
    setSelected(new Set());
    setResults(undefined);
    readPage({ channel, from, to });
  }

  function toggleVisible() {
    setSelected(previous => {
      const next = new Set(previous);
      if (allVisibleSelected || previous.size === batchLimit) {
        for (const item of visible) next.delete(item.url);
      } else {
        for (const item of visible) {
          if (next.size >= batchLimit) break;
          next.add(item.url);
        }
      }
      return next;
    });
  }

  function select(item: Attachment) {
    setSelected(previous => {
      const next = new Set(previous);
      if (!next.delete(item.url) && previous.size < batchLimit) next.add(item.url);
      return next;
    });
  }

  async function downloadSelected() {
    const chosen = [...selected];
    if (chosen.length > 0 && await onQueue(chosen)) {
      setSelected(previous => new Set([...previous].filter(url => !selected.has(url))));
    }
  }

  return (
    <div className="channel-browser">
      {saved ? (
        <div className="saved-attachments-toolbar">
          <button className="secondary-button" type="button" disabled={Boolean(pageState) || busy} onClick={refreshSavedMessages}>
            <RotateCw aria-hidden="true" />{t('refreshSavedMessages')}
          </button>
        </div>
      ) : (
        <form className="channel-query" onSubmit={browse} noValidate>
          <label className="browser-field channel-source">
            <span>{t('channel')}</span>
            <select value={manual ? 'manual' : source} onChange={event => { setSource(event.currentTarget.value); editQuery(); }}>
              <option value="" disabled>{t('chooseChannel')}</option>
              {channels?.map(channel => (
                <option key={channel.id} value={channel.id}>{channel.title}{channel.username ? ` (@${channel.username})` : ''}</option>
              ))}
              <option value="manual">{t('useChannelLink')}</option>
            </select>
          </label>
          {manual && (
            <label className="browser-field channel-address">
              <span>{t('channelLink')}</span>
              <input type="text" value={channelLink} onChange={event => { setChannelLink(event.currentTarget.value); editQuery(); }} placeholder={t('channelPlaceholder')} maxLength={2048} autoComplete="off" autoCapitalize="off" spellCheck={false} />
            </label>
          )}
          <div className="channel-dates">
            <label className="browser-field">
              <span>{t('startDate')}</span>
              <input type="date" value={fromDay} max={toDay || today} onChange={event => { setFromDay(event.currentTarget.value); editQuery(); }} />
            </label>
            <label className="browser-field">
              <span>{t('endDate')}</span>
              <input type="date" value={toDay} min={fromDay} max={today} onChange={event => { setToDay(event.currentTarget.value); editQuery(); }} />
            </label>
            <button className="primary-button" type="submit" disabled={Boolean(pageState) || busy}><Search aria-hidden="true" />{t('browse')}</button>
          </div>
          {channelsState && (
            <p className="channel-hint" role="status">
              {t(channelsState === 'failed' ? 'channelsUnavailable' : channelsState === 'waiting' ? 'browserWaiting' : 'channelsLoading')}
            </p>
          )}
        </form>
      )}
      {error && <p className="form-error" role="alert">{saved && error.code === 'browse_unavailable'
        ? t('savedMessagesUnavailable') : failureText(language, error.code)}</p>}
      {results && (
        <section className="attachment-results" aria-label={t(saved ? 'savedMessages' : 'channelAttachments')} aria-busy={Boolean(pageState)}>
          <div className="attachment-tools">
            <label className="attachment-check">
              <input ref={selectVisible} type="checkbox" checked={allVisibleSelected} onChange={toggleVisible} disabled={visible.length === 0 || busy || (selected.size === batchLimit && !visibleSelected)} />
              {t('selectVisible')}
            </label>
            <label className="attachment-types">
              <span>{t('fileType')}</span>
              <select value={category} onChange={event => setCategory(event.currentTarget.value as Category)}>
                <option value="all">{t('allTypes')}</option>
                {categories.map(kind => <option key={kind} value={kind}>{t(kind)}</option>)}
              </select>
            </label>
          </div>
          {visible.length === 0 && (
            <p className="channel-empty">{t(category !== 'all' || results.nextBefore ? 'noTypeMatches' : saved ? 'savedMessagesEmpty' : 'noAttachments')}</p>
          )}
          {visible.length > 0 && (
            <ul className="attachment-list">
              {visible.map(item => {
                const title = item.name || item.title || t('telegramAttachment');
                const time = duration(item.duration);
                const checked = selected.has(item.url);
                return (
                  <li className="attachment-row" key={item.url} data-selected={checked}>
                    <label className="attachment-entry" title={title}>
                      <input type="checkbox" checked={checked} onChange={() => select(item)} disabled={busy || (!checked && selected.size >= batchLimit)} aria-label={title} />
                      <span className="thumb" aria-hidden="true">
                        <MediaArtwork cover={imageSource(item.thumbnail)} kind={item.kind} />
                        {time && <span className="duration">{time}</span>}
                      </span>
                      <span className="attachment-caption">
                        <strong>{title}</strong>
                        <span>{[t(item.kind), filesize(item.size), item.date > 0 && date.format(item.date * 1000)].filter(Boolean).join(' · ')}</span>
                      </span>
                    </label>
                    {item.kind === 'video' && (
                      <button className="row-action" type="button" aria-label={`${t('preview')} · ${title}`} title={t('preview')} aria-haspopup="dialog" disabled={busy || Boolean(pageState)} onClick={() => onPreview(item)}>
                        <Play aria-hidden="true" /><span>{t('preview')}</span>
                      </button>
                    )}
                  </li>
                );
              })}
            </ul>
          )}
          {results.nextBefore && !pageState && (
            <button className="secondary-button load-earlier" type="button" disabled={busy} onClick={() => readPage(results.query, results.nextBefore)}>
              <ChevronDown aria-hidden="true" />{t('loadEarlier')}
            </button>
          )}
        </section>
      )}
      {pageState && (
        <div className="browse-progress">
          <p role="status">{t(pageState === 'waiting' ? 'browserWaiting' : saved ? 'savedMessagesLoading' : 'attachmentsLoading')}</p>
          <button className="secondary-button" type="button" onClick={cancelPage}>{t('cancel')}</button>
        </div>
      )}
      {results && (
        <footer className="attachment-footer">
          <div>
            <div className="attachment-selection">
              <strong>{t('selected').replace('{count}', String(selected.size))}</strong>
              {selected.size === batchLimit && <span>{t('selectionLimit')}</span>}
            </div>
            {destination}
          </div>
          <button className="primary-button" type="button" disabled={selected.size === 0 || busy} onClick={() => { void downloadSelected(); }}>
            <ArrowDownToLine aria-hidden="true" />{t('addSelected')}
          </button>
          <div className="notifications" role="status" aria-live="polite">
            {notification && <span>{notification}</span>}
          </div>
        </footer>
      )}
    </div>
  );
}
