// Shapes of the hub's /api/live payload (internal/hub and internal/airband in Go).

export interface StreamInfo {
  name: string;
  genre: string;
  description: string;
  content_type: string;
  audio_info: string;
}

export interface Mount {
  mount: string;
  info: StreamInfo;
  title: string;
  title_updated: string;
  source_connected: boolean;
  source_since: string;
  listeners: number;
  bytes_in: number;
}

export interface Receiver {
  ok: boolean;
  error?: string;
  version: string;
  updated_at: string;
  clipping_ratio: number | null;
  input_dbfs: number | null;
  overflows: number;
}

export interface Frequency {
  freq: string;
  label: string;
  signal_dbfs: number;
  noise_dbfs: number;
  squelch_dbfs: number;
  open: boolean;
  tx_last_hour: number;
  airtime_last_hour_seconds: number;
}

export interface Transmission {
  start: string;
  end: string;
  freq: string;
  label: string;
  duration_seconds: number;
  snr_min_db: number;
  snr_max_db: number;
  approximate: boolean;
}

export interface Live {
  time: string;
  mounts: Mount[];
  metrics_enabled: boolean;
  recordings_enabled: boolean;
  bookmarks_enabled: boolean;
  receiver: Receiver;
  frequencies: Frequency[];
  recent: Transmission[];
}

// /api/recordings

export interface Recording {
  path: string;
  name: string;
  start: string;
  freq?: string;
  size: number;
  modified: string;
  growing: boolean;
}

export interface Day {
  date: string; // YYYY-MM-DD in the viewer's time zone
  count: number;
}

export interface Segment {
  start: number; // seconds from the start of the recording
  end: number;
}

export interface Waveform {
  duration: number;
  sample_rate: number;
  peaks_per_second: number;
  peaks: string; // base64, one byte per bucket: 0 = below -60 dBFS, 1..255 = -60..0 dBFS
  transmissions: Segment[];
}

// /api/bookmarks

export interface Bookmark {
  id: number;
  name: string;
  path: string; // the recording's final name, without .tmp
  offset: number; // seconds into the recording
  at: string; // the moment itself
  freq?: string;
  created: string;
  recording: Recording | null; // null once the file is gone
}

// Sends a JSON request; resolves to the decoded body, or null for 204 No Content.
export async function apiSend<T>(method: string, url: string, body?: unknown): Promise<T | null> {
  const r = await fetch(url, {
    method,
    headers: body === undefined ? undefined : { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!r.ok) throw new Error((await r.text()).trim() || r.statusText);
  return r.status === 204 ? null : ((await r.json()) as T);
}
