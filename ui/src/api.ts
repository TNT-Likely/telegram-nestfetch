import { invoke, isTauri } from '@tauri-apps/api/core';
import { getCurrentWindow } from '@tauri-apps/api/window';

export type Theme = 'light' | 'dark' | 'system';

export interface Failure {
  code: string;
  message?: string;
}

export type AccountState =
  | 'setup'
  | 'disconnected'
  | 'starting'
  | 'qr'
  | 'password'
  | 'connected'
  | 'error';

export interface Account {
  state: AccountState;
  backend?: 'tdl';
  preconfigured?: boolean;
  label?: string;
  qrImage?: string;
  qrUrl?: string;
  expires?: number;
  error?: Failure;
}

export type JobState =
  | 'queued'
  | 'resolving'
  | 'downloading'
  | 'paused'
  | 'saving'
  | 'waiting_save'
  | 'completed'
  | 'failed'
  | 'removing';

export type MediaKind = 'video' | 'image' | 'audio' | 'file';

export interface Job {
  id: string;
  url: string;
  title: string;
  source: string;
  name: string;
  kind?: MediaKind;
  size: number;
  duration: number;
  downloaded: number;
  speed: number;
  state: JobState;
  folder: string;
  outputPath?: string;
  error?: Failure;
  hasThumbnail: boolean;
  hidden?: boolean;
  transferDone?: boolean;
}

export interface Channel {
  id: string;
  title: string;
  username?: string;
}

export interface Attachment {
  url: string;
  title: string;
  name: string;
  kind: MediaKind;
  size: number;
  date: number;
  duration: number;
  thumbnail?: string;
}

export interface AttachmentQuery {
  channel: string;
  from: number;
  to: number;
}

export interface BatchResult {
  added: number;
  duplicates: number;
}

export interface TaskReference { id: string }
type TaskStatus<Result> =
  | { state: 'loading'; waiting?: boolean }
  | { state: 'failed'; error?: Failure }
  | ({ state: 'ready' } & Result);

export interface ProxySettings {
  address?: string;
  authenticated?: boolean;
}

export interface Snapshot {
  account: Account;
  settings: { folder: string; proxy: ProxySettings };
  jobs: Job[];
}

interface EngineMethods {
  snapshot: [undefined, Snapshot];
  configure: [{ apiId: number; apiHash: string }, null];
  begin_login: [undefined, null];
  login_password: [{ password: string }, null];
  cancel_login: [undefined, null];
  sign_out: [undefined, null];
  set_folder: [{ folder: string }, null];
  set_proxy: [{ address: string }, boolean];
  test_proxy: [{ address?: string }, null];
  enqueue: [{ url: string }, null];
  enqueue_batch: [{ urls: string[] }, BatchResult];
  channels_start: [undefined, TaskReference];
  channels_status: [TaskReference, TaskStatus<{ channels: Channel[] }>];
  attachments_start: [AttachmentQuery & { before?: number }, TaskReference];
  attachments_status: [TaskReference, TaskStatus<{ items: Attachment[]; nextBefore?: number }>];
  browse_cancel: [TaskReference, null];
  preview_start: [{ url: string }, TaskReference];
  local_preview_start: [{ id: string }, TaskReference];
  preview_status: [TaskReference, TaskStatus<{ url: string; speed?: number; bytesRead?: number }>];
  preview_stop: [TaskReference, null];
  pause: [{ id: string }, null];
  resume: [{ id: string }, null];
  retry: [{ id: string }, null];
  hide_job: [{ id: string }, null];
  restore_job: [{ id: string }, null];
  remove_job: [{ id: string; discardStaged?: boolean }, null];
  save_elsewhere: [{ id: string; folder: string }, null];
  thumbnail: [{ id: string }, string | null];
}

export function request<M extends keyof EngineMethods>(method: M, params: EngineMethods[M][0]) {
  return invoke<EngineMethods[M][1]>('engine_request', { method, params });
}

export function startTask<Result>(
  start: () => Promise<TaskReference>,
  poll: (reference: TaskReference) => Promise<TaskStatus<Result>>,
  cancel: (reference: TaskReference) => Promise<null>,
  onWaiting: (waiting: boolean) => void,
) {
  const reference = start();
  let stopping: Promise<void> | undefined;
  const result = reference.then(async id => {
    while (!stopping) {
      const status = await poll(id);
      if (stopping) return;
      if (status.state === 'ready') return status;
      if (status.state === 'failed') throw status.error ?? { code: 'unexpected' };
      onWaiting(Boolean(status.waiting));
      await new Promise(resolve => window.setTimeout(resolve, 420));
    }
  });
  return {
    result,
    stop() {
      stopping ??= reference.then(cancel).then(() => undefined, () => undefined);
      return stopping;
    },
  };
}

export function chooseFolder(initial: string) {
  return invoke<string | null>('choose_folder', { initial });
}

export function revealJob(id: string) {
  return invoke<void>('reveal_job', { id });
}

export function openTelegramSettings() {
  return invoke<void>('open_telegram_settings');
}

export function openProjectRepository() {
  return invoke<void>('open_project_repository');
}

export async function setWindowTheme(theme: Theme) {
  if (isTauri()) await getCurrentWindow().setTheme(theme === 'system' ? null : theme);
}

export function failureOf(value: unknown, fallback = 'unexpected'): Failure {
  if (value && typeof value === 'object' && 'code' in value && typeof value.code === 'string') {
    return { code: value.code };
  }
  if (typeof value === 'string' && /^[a-z_]+$/.test(value)) return { code: value };
  return { code: fallback };
}

export function imageSource(value: unknown): string | undefined {
  if (typeof value === 'string' && value.length <= 2_000_000 && /^data:image\/(?:jpeg|png|webp);base64,[A-Za-z0-9+/]+={0,2}$/.test(value)) {
    return value;
  }
}
