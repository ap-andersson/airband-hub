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
  receiver: Receiver;
  frequencies: Frequency[];
  recent: Transmission[];
}
