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
  label?: string;
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
  | 'failed';

export interface Job {
  id: string;
  url: string;
  title: string;
  source: string;
  name: string;
  size: number;
  duration: number;
  downloaded: number;
  speed: number;
  state: JobState;
  folder: string;
  outputPath?: string;
  error?: Failure;
  hasThumbnail: boolean;
}

export interface Snapshot {
  account: Account;
  settings: { folder: string };
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
  enqueue: [{ url: string }, null];
  pause: [{ id: string }, null];
  resume: [{ id: string }, null];
  retry: [{ id: string }, null];
  save_elsewhere: [{ id: string; folder: string }, null];
  thumbnail: [{ id: string }, string | null];
}

export function request<M extends keyof EngineMethods>(method: M, params: EngineMethods[M][0]) {
  return invoke<EngineMethods[M][1]>('engine_request', { method, params });
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
