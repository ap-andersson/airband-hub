// Shared by the Stats and Activity pages: the shape of /api/activity, how hours count as up or
// down, colours per frequency, and small chart helpers.
import type { ComponentChildren, RefObject } from "preact";
import { useEffect, useRef, useState } from "preact/hooks";
import { stationName } from "./format";

export const tz = Intl.DateTimeFormat().resolvedOptions().timeZone;
export const RANGES = [7, 30, 90] as const;
export const LIST_LIMIT = 100; // rows shown before "Show all"
export type Measure = "count" | "airtime";

export interface Frequency {
  freq: string;
  label: string;
}

export type Hours = number[]; // 24 values

export interface ActivityData {
  days: string[]; // YYYY-MM-DD, oldest first
  frequencies: Frequency[];
  counts: Record<string, Hours[]>;
  airtime: Record<string, Hours[]>;
  uptime: Hours[]; // seconds per hour
  history_since?: string; // when the stored transmissions begin
  uptime_since?: string; // when uptime began to be recorded
}

// what is known about one hour of one day
export type Slot = "nodata" | "future" | "up" | "down";

export const dayFmt = new Intl.DateTimeFormat([], { weekday: "short", day: "numeric", month: "short" });
export const longDayFmt = new Intl.DateTimeFormat([], { weekday: "long", day: "numeric", month: "long" });
export const localDate = (d: string) => new Date(`${d}T12:00:00`);
export const hh = (h: number) => String(h).padStart(2, "0");
export const today = () => {
  const d = new Date();
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
};

// colour follows the frequency (its place in the hub's list), never its rank
export const seriesColor = (i: number) => `var(--series-${(i % 4) + 1})`;

export function stored<T extends string>(key: string, allowed: readonly T[], fallback: T): [T, (v: T) => void] {
  const [v, setV] = useState<T>(() => {
    try {
      const s = localStorage.getItem(key) as T | null;
      return s !== null && allowed.includes(s) ? s : fallback;
    } catch {
      return fallback;
    }
  });
  const set = (n: T) => {
    setV(n);
    try {
      localStorage.setItem(key, n);
    } catch {
      /* not remembered, still applied */
    }
  };
  return [v, set];
}

/**
 * The share of each hour the receiver was working, 0..1, and what is known about the hour.
 * Hours before the history begins have no data and are left out of averages. Hours between
 * that and the start of uptime recording count as up, since their transmissions are there.
 */
export function upShare(a: ActivityData): { share: Hours[]; slot: Slot[][] } {
  const now = Date.now();
  const history = a.history_since ? new Date(a.history_since).getTime() : Infinity;
  const uptimeFrom = a.uptime_since ? new Date(a.uptime_since).getTime() : Infinity;
  const share: Hours[] = [];
  const slot: Slot[][] = [];
  a.days.forEach((d, i) => {
    share.push([]);
    slot.push([]);
    for (let h = 0; h < 24; h++) {
      const start = new Date(`${d}T${hh(h)}:00:00`).getTime();
      const end = start + 3_600_000;
      let v: number;
      let s: Slot;
      if (start > now) [v, s] = [0, "future"];
      else if (end <= history) [v, s] = [0, "nodata"];
      // before uptime was recorded, and its first (partly recorded) hour, count as up
      else if (start <= uptimeFrom) [v, s] = [1, "up"];
      else {
        v = Math.min(1, a.uptime[i][h] / 3600);
        // the hour under way counts the part that has passed
        if (end > now) v = Math.min(1, v / Math.max(1 / 60, (now - start) / 3_600_000));
        s = v < 0.5 ? "down" : "up";
      }
      share[i].push(v);
      slot[i].push(s);
    }
  });
  return { share, slot };
}

export function useWidth(): [RefObject<HTMLDivElement | null>, number] {
  const ref = useRef<HTMLDivElement>(null);
  const [w, setW] = useState(0);
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    const ro = new ResizeObserver(([e]) => setW(Math.floor(e.contentRect.width)));
    ro.observe(el);
    return () => ro.disconnect();
  }, []);
  return [ref, w];
}

// a round axis maximum and its tick step
export function niceScale(max: number): { top: number; step: number } {
  if (max <= 0) return { top: 1, step: 1 };
  const raw = max / 4;
  const mag = 10 ** Math.floor(Math.log10(raw));
  const step = [1, 2, 2.5, 5, 10].map((m) => m * mag).find((s) => s >= raw)!;
  return { top: Math.ceil(max / step) * step, step };
}

export function Tooltip({ x, y, width, children }: { x: number; y: number; width: number; children: ComponentChildren }) {
  const left = x > width - 200 ? undefined : x + 12;
  const right = x > width - 200 ? width - x + 12 : undefined;
  return (
    <div class="chart-tip" role="status" style={{ left, right, top: Math.max(0, y - 10) }}>
      {children}
    </div>
  );
}

/** /api/activity for the last `days` days (null: nothing yet), looked at again every minute. */
export function useActivityData(days: number | null): { data: ActivityData | null; error: string | null } {
  const [state, setState] = useState<{ data: ActivityData | null; error: string | null }>({ data: null, error: null });
  const [refresh, setRefresh] = useState(0);
  useEffect(() => {
    const id = setInterval(() => setRefresh((n) => n + 1), 60_000);
    return () => clearInterval(id);
  }, []);
  useEffect(() => {
    if (days === null) return;
    const ctl = new AbortController();
    fetch(`/api/activity?days=${days}&tz=${encodeURIComponent(tz)}`, { signal: ctl.signal })
      .then((r) => (r.ok ? r.json() : r.text().then((t) => Promise.reject(new Error(t.trim() || r.statusText)))))
      .then((data: ActivityData) => setState({ data, error: null }))
      .catch((e) => !ctl.signal.aborted && setState((s) => ({ data: s.data, error: String(e.message ?? e) })));
    return () => ctl.abort();
  }, [days, refresh]);
  return state;
}

// With onOnly, each entry is a button that shows only that frequency; clicking it again shows all.
export function Legend({ frequencies, only, onOnly }: { frequencies: Frequency[]; only?: string | null; onOnly?: (freq: string | null) => void }) {
  return (
    <ul class={onOnly ? "chart-legend pickable" : "chart-legend"}>
      {frequencies.map((f, i) => {
        const content = (
          <>
            <span class="swatch" style={{ background: seriesColor(i) }} aria-hidden="true" />
            <span class="mhz-small">{f.freq}</span> {stationName(f.label, f.freq)}
          </>
        );
        return (
          <li key={f.freq} class={only && only !== f.freq ? "dimmed" : undefined}>
            {onOnly ? (
              <button
                type="button"
                aria-pressed={only === f.freq}
                title={only === f.freq ? "Show all frequencies" : `Show only ${f.freq}`}
                onClick={() => onOnly(only === f.freq ? null : f.freq)}
              >
                {content}
              </button>
            ) : (
              content
            )}
          </li>
        );
      })}
      {onOnly && only && (
        <li>
          <button type="button" class="show-all-freqs" onClick={() => onOnly(null)}>
            Show all
          </button>
        </li>
      )}
    </ul>
  );
}

