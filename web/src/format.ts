export const clockFmt = new Intl.DateTimeFormat([], { hour: "2-digit", minute: "2-digit", second: "2-digit", hourCycle: "h23" });
export const dayFmt = new Intl.DateTimeFormat([], { weekday: "long", day: "numeric", month: "long", year: "numeric" });

// Labels often repeat the frequency ("MALMEN TOWER 129.800"), which the strip already shows.
export function stationName(label: string, freq: string): string {
  return label.replace(freq, "").trim() || label;
}

export function seconds(s: number): string {
  if (s < 60) return `${s.toFixed(s < 10 ? 1 : 0)} s`;
  const m = Math.floor(s / 60);
  return `${m} min ${Math.round(s % 60)} s`;
}

export function ago(iso: string, now: Date): string {
  const s = Math.max(0, (now.getTime() - new Date(iso).getTime()) / 1000);
  if (s < 10) return "just now";
  if (s < 60) return `${Math.round(s)} s ago`;
  if (s < 3600) return `${Math.round(s / 60)} min ago`;
  if (s < 86400) return `${Math.round(s / 3600)} h ago`;
  return `${Math.round(s / 86400)} days ago`;
}

export function clock(iso: string): string {
  return new Date(iso).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit", hourCycle: "h23" });
}

export function snr(min: number, max: number): string {
  if (min === 0 && max === 0) return "–";
  if (min === max) return `≥ ${min} dB`;
  return `${min}–${max} dB`;
}

// Go's zero time.Time marshals as year 1.
export function isSet(iso: string): boolean {
  return !!iso && !iso.startsWith("0001-");
}
