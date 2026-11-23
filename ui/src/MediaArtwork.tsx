import { useState } from 'react';
import { FileText, Film, Image, Music2 } from 'lucide-react';
import type { MediaKind } from './api';

const icons = { video: Film, image: Image, audio: Music2, file: FileText };

export function MediaArtwork({ cover, kind }: { cover?: string; kind?: MediaKind }) {
  const [failed, setFailed] = useState<string>();
  const Icon = icons[kind ?? 'file'];
  return cover && failed !== cover
    ? <img alt="" decoding="async" draggable={false} src={cover} onError={() => setFailed(cover)} />
    : <Icon className="media-placeholder" aria-hidden="true" />;
}

export function filesize(bytes: number) {
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

export function duration(seconds: number) {
  if (!Number.isFinite(seconds) || seconds <= 0) return null;
  const whole = Math.floor(seconds);
  const values: number[] = [Math.floor(whole / 60) % 60, whole % 60];
  if (whole >= 3600) values.unshift(Math.floor(whole / 3600));
  return values.map(value => String(value).padStart(2, '0')).join(':');
}
