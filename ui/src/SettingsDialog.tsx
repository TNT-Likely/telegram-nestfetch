import { useEffect, useRef, useState } from 'react';
import type { FormEvent } from 'react';
import { Channel, invoke } from '@tauri-apps/api/core';
import { ExternalLink, X } from 'lucide-react';
import { failureOf, request } from './api';
import type { ProxySettings } from './api';
import { failureText } from './i18n';
import type { Language, Translate } from './i18n';
import { filesize } from './MediaArtwork';

type Operation = 'test' | 'save' | 'check' | 'install';
type ProxyFeedback = { success: 'proxySaved' | 'proxyTestPassed' } | { error: string };

interface UpdateInfo {
  version: string;
  installable: boolean;
}

type UpdateProgress =
  | { event: 'started'; totalBytes: number | null }
  | { event: 'progress'; downloadedBytes: number; totalBytes: number | null }
  | { event: 'installing' };

interface Props {
  proxy: ProxySettings | undefined;
  language: Language;
  t: Translate;
  onProxyChanged: () => Promise<void>;
  onClose: () => void;
}

export function SettingsDialog({ proxy, language, t, onProxyChanged, onClose }: Props) {
  const dialog = useRef<HTMLDialogElement>(null);
  const initialProxy = useRef(proxy);
  const [manual, setManual] = useState(Boolean(proxy?.address));
  const [address, setAddress] = useState(proxy?.authenticated ? '' : (proxy?.address ?? ''));
  const [proxyFeedback, setProxyFeedback] = useState<ProxyFeedback>();
  const [update, setUpdate] = useState<UpdateInfo | null>();
  const [updateError, setUpdateError] = useState<string>();
  const [progress, setProgress] = useState<UpdateProgress>();
  const [operation, setOperation] = useState<Operation>();

  const draft = address.trim();
  const savedAddress = proxy?.address ?? '';
  const canSave = manual ? Boolean(draft && (draft !== savedAddress || proxy?.authenticated)) : Boolean(savedAddress);
  const canTest = !manual || Boolean(draft || savedAddress);
  const updating = operation === 'install';

  useEffect(() => { dialog.current?.showModal(); }, []);
  useEffect(() => {
    if (!proxy || initialProxy.current) return;
    initialProxy.current = proxy;
    setManual(Boolean(proxy.address));
    setAddress(proxy.authenticated ? '' : (proxy.address ?? ''));
  }, [proxy]);

  async function testProxy() {
    setOperation('test');
    setProxyFeedback(undefined);
    try {
      await request('test_proxy', manual && !draft ? {} : { address: manual ? draft : '' });
      setProxyFeedback({ success: 'proxyTestPassed' });
    } catch (error) {
      setProxyFeedback({ error: failureOf(error, 'proxy_test_failed').code });
    } finally {
      setOperation(undefined);
    }
  }

  async function saveProxy(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (operation || !proxy || !canSave) return;
    setOperation('save');
    setProxyFeedback(undefined);
    try {
      if (await request('set_proxy', { address: manual ? draft : '' })) await onProxyChanged();
      if (!manual || draft.includes('@')) setAddress('');
      setProxyFeedback({ success: 'proxySaved' });
    } catch (error) {
      setProxyFeedback({ error: failureOf(error, 'local_storage').code });
    } finally {
      setOperation(undefined);
    }
  }

  async function checkUpdates() {
    setOperation('check');
    setUpdateError(undefined);
    setUpdate(undefined);
    try {
      setUpdate(await invoke<UpdateInfo | null>('check_updates'));
    } catch (error) {
      setUpdateError(failureOf(error, 'update_check_failed').code);
    } finally {
      setOperation(undefined);
    }
  }

  async function installUpdate() {
    const onProgress = new Channel<UpdateProgress>();
    onProgress.onmessage = setProgress;
    setOperation('install');
    setProgress(undefined);
    setUpdateError(undefined);
    try {
      await invoke('install_update', { onProgress });
    } catch (error) {
      setUpdateError(failureOf(error, 'update_install_failed').code);
    } finally {
      setOperation(undefined);
    }
  }

  async function openReleasePage() {
    try {
      await invoke('open_release_page');
    } catch (error) {
      setUpdateError(failureOf(error, 'open_release_failed').code);
    }
  }

  const installing = progress?.event === 'installing';
  const bytes = progress?.event === 'progress' ? progress.downloadedBytes : 0;
  const total = progress?.event === 'progress' || progress?.event === 'started' ? progress.totalBytes : null;

  return (
    <dialog className="account-dialog settings-dialog" ref={dialog} aria-labelledby="settings-title"
      onCancel={event => { event.preventDefault(); if (!operation) onClose(); }}>
      <div className="dialog-head">
        <h2 id="settings-title">{t('appSettings')}</h2>
        <button type="button" className="icon-button" aria-label={t('close')} disabled={Boolean(operation)} onClick={onClose}><X aria-hidden="true" /></button>
      </div>
      <section className="settings-section" aria-labelledby="proxy-title">
        <h3 id="proxy-title">{t('telegramProxy')}</h3>
        <form className="dialog-form" onSubmit={saveProxy}>
          <label>
            <span id="proxy-mode-label">{t('connectionMode')}</span>
            <select aria-labelledby="proxy-mode-label" value={manual ? 'manual' : 'direct'} disabled={!proxy || Boolean(operation)} onChange={event => {
              setManual(event.currentTarget.value === 'manual'); setProxyFeedback(undefined);
            }}>
              <option value="direct">{t('directConnection')}</option>
              <option value="manual">{t('manualProxy')}</option>
            </select>
          </label>
          {manual && (
            <label>
              <span id="proxy-address-label">{t('proxyAddress')}</span>
              <input aria-labelledby="proxy-address-label" value={address} type={draft.includes('@') ? 'password' : 'text'} autoComplete="off" autoCapitalize="off" spellCheck={false}
                placeholder={savedAddress || 'socks5://127.0.0.1:7890'} disabled={Boolean(operation)} maxLength={4096}
                aria-describedby="proxy-address-hint" onChange={event => { setAddress(event.currentTarget.value); setProxyFeedback(undefined); }} />
              <span className="dialog-copy" id="proxy-address-hint">{t(proxy?.authenticated ? 'proxySavedCredentials' : 'proxyAddressHint')}</span>
            </label>
          )}
          {proxyFeedback && ('error' in proxyFeedback
            ? <p className="dialog-error" role="alert">{failureText(language, proxyFeedback.error)}</p>
            : <p className="dialog-copy" role="status">{t(proxyFeedback.success)}</p>)}
          <div className="dialog-actions dialog-actions-wrap">
            <button type="button" className="secondary-button" disabled={!proxy || !canTest || Boolean(operation)} onClick={() => { void testProxy(); }}>
              {t(operation === 'test' ? 'testingProxy' : 'testProxy')}
            </button>
            <button type="submit" className="primary-button" disabled={!proxy || !canSave || Boolean(operation)}>
              {t(operation === 'save' ? 'savingProxy' : 'saveProxy')}
            </button>
          </div>
        </form>
      </section>
      <section className="settings-section update-section" aria-labelledby="update-title">
        <div className="settings-section-title">
          <h3 id="update-title">{t('softwareUpdate')}</h3>
          <span aria-label={t('appVersion')}>v{import.meta.env.VITE_APP_VERSION}</span>
        </div>
        {updating ? (
          <div className="update-progress">
            <p role="status" className="dialog-copy">{t(installing ? 'updateInstalling' : 'updateDownloading')}
              {!installing && bytes > 0 && ` ${filesize(bytes)}${total ? ` / ${filesize(total)}` : ''}`}
            </p>
            <progress aria-label={t(installing ? 'updateInstalling' : 'updateDownloading')} max={total || 1} value={!installing && total ? bytes : undefined} />
          </div>
        ) : update !== undefined && (
          <p className="dialog-copy" role="status">
            {update ? t('updateAvailable').replace('{version}', 'v' + update.version) : t('updateCurrent')}
            {update && <span className="update-hint">{t(update.installable ? 'updateRestartHint' : 'updatePackageHint')}</span>}
          </p>
        )}
        {updateError && <p className="dialog-error" role="alert">{failureText(language, updateError)}</p>}
        <div className="dialog-actions dialog-actions-wrap">
          {(updateError || (update && !update.installable)) && (
            <button type="button" className="secondary-button" disabled={Boolean(operation)} onClick={() => { void openReleasePage(); }}>
              <ExternalLink aria-hidden="true" />{t('releasePage')}
            </button>
          )}
          <button type="button" className="secondary-button" disabled={Boolean(operation)} onClick={() => { void checkUpdates(); }}>
            {t(operation === 'check' ? 'checkingUpdates' : 'checkUpdates')}
          </button>
          {update?.installable && (
            <button type="button" className="primary-button" disabled={Boolean(operation)} onClick={() => { void installUpdate(); }}>{t('installUpdate')}</button>
          )}
        </div>
      </section>
    </dialog>
  );
}
