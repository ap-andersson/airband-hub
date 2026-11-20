// The Activity page: the stored transmission history (up to 90 days) as an average day, daily
// totals, a day × hour heatmap and each day's full list, which plays from the recordings.
import type { ComponentChildren, RefObject } from "preact";
import { useEffect, useMemo, useRef, useState } from "preact/hooks";
import type { Recording, Transmission } from "./api";
import { clock, seconds, snr, stationName } from "./format";
import { LEAD_IN, usePlayer } from "./player";
import { loadWave } from "./Recordings";

const tz = Intl.DateTimeFormat().resolvedOptions().timeZone;
const RANGES = [7, 30, 90] as const;
const LIST_LIMIT = 100; // rows shown before "Show all"
type Measure = "count" | "airtime";

interface Frequency {
  freq: string;
  label: string;
}

type Hours = number[]; // 24 values

interface ActivityData {
  days: string[]; // YYYY-MM-DD, oldest first
  frequencies: Frequency[];
  counts: Record<string, Hours[]>;
  airtime: Record<string, Hours[]>;
  uptime: Hours[]; // seconds per hour
  history_since?: string; // when the stored transmissions begin
  uptime_since?: string; // when uptime began to be recorded
}

// what is known about one hour of one day
type Slot = "nodata" | "future" | "up" | "down";

const dayFmt = new Intl.DateTimeFormat([], { weekday: "short", day: "numeric", month: "short" });
const longDayFmt = new Intl.DateTimeFormat([], { weekday: "long", day: "numeric", month: "long" });
const localDate = (d: string) => new Date(`${d}T12:00:00`);
const hh = (h: number) => String(h).padStart(2, "0");
const today = () => {
  const d = new Date();
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
};

// colour follows the frequency (its place in the hub's list), never its rank
const seriesColor = (i: number) => `var(--series-${(i % 4) + 1})`;

function stored<T extends string>(key: string, allowed: readonly T[], fallback: T): [T, (v: T) => void] {
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
function upShare(a: ActivityData): { share: Hours[]; slot: Slot[][] } {
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

function useWidth(): [RefObject<HTMLDivElement | null>, number] {
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
function niceScale(max: number): { top: number; step: number } {
  if (max <= 0) return { top: 1, step: 1 };
  const raw = max / 4;
  const mag = 10 ** Math.floor(Math.log10(raw));
  const step = [1, 2, 2.5, 5, 10].map((m) => m * mag).find((s) => s >= raw)!;
  return { top: Math.ceil(max / step) * step, step };
}

function Tooltip({ x, y, width, children }: { x: number; y: number; width: number; children: ComponentChildren }) {
  const left = x > width - 200 ? undefined : x + 12;
  const right = x > width - 200 ? width - x + 12 : undefined;
  return (
    <div class="chart-tip" role="status" style={{ left, right, top: Math.max(0, y - 10) }}>
      {children}
    </div>
  );
}

export function Activity({ enabled }: { enabled: boolean }) {
  const [range, setRange] = stored<string>("hub.activityRange", RANGES.map(String), "30");
  const [measure, setMeasure] = stored<Measure>("hub.activityMeasure", ["count", "airtime"], "count");
  const [data, setData] = useState<ActivityData | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [day, setDay] = useState(today());
  const [refresh, setRefresh] = useState(0);
  const listRef = useRef<HTMLElement>(null);

  useEffect(() => {
    if (!enabled) return;
    const ctl = new AbortController();
    fetch(`/api/activity?days=${range}&tz=${encodeURIComponent(tz)}`, { signal: ctl.signal })
      .then((r) => (r.ok ? r.json() : r.text().then((t) => Promise.reject(new Error(t.trim() || r.statusText)))))
      .then((d: ActivityData) => {
        setData(d);
        setError(null);
      })
      .catch((e) => !ctl.signal.aborted && setError(String(e.message ?? e)));
    return () => ctl.abort();
  }, [enabled, range, refresh]);

  // today keeps growing; look again every minute
  useEffect(() => {
    const id = setInterval(() => setRefresh((n) => n + 1), 60_000);
    return () => clearInterval(id);
  }, []);

  const pickDay = (d: string) => {
    setDay(d);
    requestAnimationFrame(() => listRef.current?.scrollIntoView({ behavior: "smooth", block: "start" }));
  };

  if (!enabled) {
    return (
      <main>
        <p class="empty">
          The activity history needs rtl_airband's metrics (<code>HUB_METRICS_URL</code>) and a data folder (<code>HUB_DATA_DIR</code>).
        </p>
      </main>
    );
  }

  return (
    <main class="activity">
      <div class="activity-head">
        <h2>Activity</h2>
        <div class="activity-controls">
          <div class="segmented" role="group" aria-label="Measure">
            <button type="button" aria-pressed={measure === "count"} onClick={() => setMeasure("count")}>
              Transmissions
            </button>
            <button type="button" aria-pressed={measure === "airtime"} onClick={() => setMeasure("airtime")}>
              Airtime
            </button>
          </div>
          <div class="segmented" role="group" aria-label="Period">
            {RANGES.map((r) => (
              <button key={r} type="button" aria-pressed={range === String(r)} onClick={() => setRange(String(r))}>
                {r} days
              </button>
            ))}
          </div>
        </div>
      </div>
      {error && <p class="empty">Cannot read the history: {error}</p>}
      {!data && !error && <p class="empty">Loading…</p>}
      {data && data.frequencies.length === 0 && <p class="empty">No transmissions recorded yet. They are kept for 90 days from when the hub first sees them.</p>}
      {data && data.frequencies.length > 0 && <Charts data={data} measure={measure} day={day} onDay={pickDay} />}
      <section ref={listRef} class="panel day-list" aria-labelledby="day-list-title">
        <DayList day={day} onDay={setDay} frequencies={data?.frequencies ?? []} />
      </section>
    </main>
  );
}

function Charts({ data, measure, day, onDay }: { data: ActivityData; measure: Measure; day: string; onDay: (d: string) => void }) {
  const { share: up, slot } = useMemo(() => upShare(data), [data]);
  const values = measure === "count" ? data.counts : data.airtime;
  const fmt = (v: number) => (measure === "count" ? (v >= 10 ? v.toFixed(0) : v.toFixed(1)) : seconds(v));
  // axis ticks: whole numbers when the step allows it
  const axisFmt = (v: number, step: number) => (measure === "count" ? v.toFixed(step >= 1 ? 0 : 1) : seconds(v));
  const total = (m: Record<string, Hours[]>) => data.frequencies.reduce((s, f) => s + m[f.freq].flat().reduce((a, b) => a + b, 0), 0);
  const upHours = up.flat().reduce((a, b) => a + b, 0);
  const knownHours = slot.flat().filter((s) => s === "up" || s === "down").length;

  // average per hour of day: the sum over the period divided by the hours the receiver was up
  const average = data.frequencies.map((f, i) => ({
    freq: f.freq,
    name: stationName(f.label, f.freq),
    color: seriesColor(i),
    values: Array.from({ length: 24 }, (_, h) => {
      const hours = up.reduce((s, d) => s + d[h], 0);
      return hours > 0 ? values[f.freq].reduce((s, d) => s + d[h], 0) / hours : 0;
    }),
  }));

  return (
    <>
      <div class="stat-tiles">
        <div class="stat">
          <span class="stat-value">{total(data.counts).toLocaleString()}</span>
          <span class="stat-label">transmissions</span>
        </div>
        <div class="stat">
          <span class="stat-value">{seconds(total(data.airtime))}</span>
          <span class="stat-label">on air</span>
        </div>
        <div class="stat">
          <span class="stat-value">{knownHours ? `${Math.round((100 * upHours) / knownHours)} %` : "–"}</span>
          <span class="stat-label">receiver up</span>
        </div>
      </div>

      <section class="panel chart-panel" aria-labelledby="avg-title">
        <h3 id="avg-title">{measure === "count" ? "Transmissions per hour, average day" : "Airtime per hour, average day"}</h3>
        <Legend frequencies={data.frequencies} />
        <HourChart series={average} fmt={fmt} axisFmt={axisFmt} />
        <details class="table-view">
          <summary>Show as a table</summary>
          <table>
            <thead>
              <tr>
                <th scope="col">Hour</th>
                {average.map((s) => (
                  <th key={s.freq} scope="col" class="num">
                    {s.freq}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {Array.from({ length: 24 }, (_, h) => (
                <tr key={h}>
                  <td>{hh(h)}:00</td>
                  {average.map((s) => (
                    <td key={s.freq} class="num">
                      {fmt(s.values[h])}
                    </td>
                  ))}
                </tr>
              ))}
            </tbody>
          </table>
        </details>
      </section>

      <section class="panel chart-panel" aria-labelledby="days-title">
        <h3 id="days-title">{measure === "count" ? "Transmissions per day" : "Airtime per day"}</h3>
        <Legend frequencies={data.frequencies} />
        <DayChart data={data} values={values} up={up} slot={slot} fmt={fmt} axisFmt={axisFmt} day={day} onDay={onDay} />
        <p class="chart-note">
          Below the bars: each hour of each day, darker for more. <span class="hatch-key" aria-hidden="true" /> the receiver was down.
        </p>
      </section>
    </>
  );
}

function Legend({ frequencies }: { frequencies: Frequency[] }) {
  return (
    <ul class="chart-legend">
      {frequencies.map((f, i) => (
        <li key={f.freq}>
          <span class="swatch" style={{ background: seriesColor(i) }} aria-hidden="true" />
          <span class="mhz-small">{f.freq}</span> {stationName(f.label, f.freq)}
        </li>
      ))}
    </ul>
  );
}

interface Series {
  freq: string;
  name: string;
  color: string;
  values: number[];
}

// Lines over the 24 hours of the day, labelled at their right end, with a crosshair.
function HourChart({ series, fmt, axisFmt }: { series: Series[]; fmt: (v: number) => string; axisFmt: (v: number, step: number) => string }) {
  const [ref, width] = useWidth();
  const [hover, setHover] = useState<number | null>(null);
  const H = 220;
  const m = { l: 40, r: width < 500 ? 12 : 120, t: 10, b: 24 };
  const iw = Math.max(1, width - m.l - m.r);
  const ih = H - m.t - m.b;
  const { top, step } = niceScale(Math.max(...series.flatMap((s) => s.values)));
  const x = (h: number) => m.l + (h / 23) * iw;
  const y = (v: number) => m.t + ih - (v / top) * ih;

  // end labels, nudged apart so they never overlap, and kept inside the plot
  const labels = series
    .map((s) => ({ s, y: y(s.values[23]) }))
    .sort((a, b) => a.y - b.y)
    .reduce<{ s: Series; y: number }[]>((out, l) => {
      const prev = out.at(-1);
      out.push({ s: l.s, y: prev && l.y - prev.y < 14 ? prev.y + 14 : l.y });
      return out;
    }, []);
  const overflow = (labels.at(-1)?.y ?? 0) - (m.t + ih);
  if (overflow > 0) labels.forEach((l) => (l.y -= overflow));

  return (
    <div class="chart" ref={ref} style={{ height: `${H}px` }}>
      {width > 0 && (
        <svg
          width={width}
          height={H}
          role="img"
          aria-label="Average per hour of the day, per frequency"
          onMouseMove={(e) => {
            const r = (e.currentTarget as SVGSVGElement).getBoundingClientRect();
            const h = Math.round(((e.clientX - r.left - m.l) / iw) * 23);
            setHover(h >= 0 && h <= 23 ? h : null);
          }}
          onMouseLeave={() => setHover(null)}
        >
          {Array.from({ length: Math.round(top / step) + 1 }, (_, i) => i * step).map((v) => (
            <g key={v}>
              <line class="grid" x1={m.l} x2={m.l + iw} y1={y(v)} y2={y(v)} />
              <text class="axis" x={m.l - 6} y={y(v) + 4} text-anchor="end">
                {axisFmt(v, step)}
              </text>
            </g>
          ))}
          {[0, 3, 6, 9, 12, 15, 18, 21].map((h) => (
            <text key={h} class="axis" x={x(h)} y={H - 6} text-anchor="middle">
              {hh(h)}
            </text>
          ))}
          {hover !== null && <line class="crosshair" x1={x(hover)} x2={x(hover)} y1={m.t} y2={m.t + ih} />}
          {series.map((s) => (
            <polyline key={s.freq} class="series-line" points={s.values.map((v, h) => `${x(h)},${y(v)}`).join(" ")} style={{ stroke: s.color }} />
          ))}
          {hover !== null && series.map((s) => <circle key={s.freq} class="series-dot" cx={x(hover)} cy={y(s.values[hover])} r={4} style={{ fill: s.color }} />)}
          {width >= 500 &&
            labels.map((l) => (
              <text key={l.s.freq} class="end-label" x={m.l + iw + 8} y={l.y + 4}>
                {l.s.name}
              </text>
            ))}
        </svg>
      )}
      {hover !== null && (
        <Tooltip x={x(hover)} y={m.t} width={width}>
          <strong>
            {hh(hover)}:00–{hh(hover)}:59
          </strong>
          {[...series]
            .sort((a, b) => b.values[hover] - a.values[hover])
            .map((s) => (
              <span key={s.freq} class="tip-row">
                <span class="swatch" style={{ background: s.color }} aria-hidden="true" />
                {s.name}
                <span class="tip-value">{fmt(s.values[hover])}</span>
              </span>
            ))}
        </Tooltip>
      )}
    </div>
  );
}

// Stacked bars per day, and under them a day × hour heatmap on the same columns.
function DayChart({
  data,
  values,
  up,
  slot,
  fmt,
  axisFmt,
  day,
  onDay,
}: {
  data: ActivityData;
  values: Record<string, Hours[]>;
  up: Hours[];
  slot: Slot[][];
  fmt: (v: number) => string;
  axisFmt: (v: number, step: number) => string;
  day: string;
  onDay: (d: string) => void;
}) {
  const [ref, width] = useWidth();
  const [hover, setHover] = useState<{ d: number; h: number | null } | null>(null);
  const n = data.days.length;
  const BAR_H = 180;
  const HEAT_H = 24 * 7;
  const m = { l: 40, r: 12, t: 10, gap: 26, b: 22 };
  const H = m.t + BAR_H + m.gap + HEAT_H + m.b;
  const iw = Math.max(1, width - m.l - m.r);
  const band = iw / n;
  const bw = Math.max(1, band - (band > 6 ? 2 : 1)); // a surface gap between bars
  const sums = data.days.map((_, d) => data.frequencies.map((f) => values[f.freq][d].reduce((a, b) => a + b, 0)));
  const { top, step } = niceScale(Math.max(...sums.map((s) => s.reduce((a, b) => a + b, 0))));
  const yb = (v: number) => m.t + BAR_H - (v / top) * BAR_H;
  const heatTop = m.t + BAR_H + m.gap;
  const cell = HEAT_H / 24;
  const heatMax = Math.max(1, ...data.days.flatMap((_, d) => Array.from({ length: 24 }, (_, h) => data.frequencies.reduce((s, f) => s + values[f.freq][d][h], 0))));
  const labelEvery = Math.ceil(n / Math.max(1, Math.floor(iw / 64)));
  const upDay = (d: number) => up[d].reduce((a, b) => a + b, 0);
  const knownDay = (d: number) => slot[d].filter((s) => s === "up" || s === "down").length;

  const at = (e: MouseEvent) => {
    const r = (e.currentTarget as SVGSVGElement).getBoundingClientRect();
    const px = e.clientX - r.left - m.l;
    const py = e.clientY - r.top;
    const d = Math.floor(px / band);
    if (d < 0 || d >= n) return null;
    const h = py >= heatTop && py < heatTop + HEAT_H ? Math.floor((py - heatTop) / cell) : null;
    return { d, h };
  };

  const tip = hover && (
    <Tooltip x={m.l + (hover.d + 0.5) * band} y={hover.h === null ? m.t : heatTop + hover.h * cell} width={width}>
      <strong>
        {dayFmt.format(localDate(data.days[hover.d]))}
        {hover.h !== null && `, ${hh(hover.h)}:00`}
      </strong>
      {data.frequencies.map((f, i) => {
        const v = hover.h === null ? sums[hover.d][i] : values[f.freq][hover.d][hover.h];
        return (
          <span key={f.freq} class="tip-row">
            <span class="swatch" style={{ background: seriesColor(i) }} aria-hidden="true" />
            {stationName(f.label, f.freq)}
            <span class="tip-value">{fmt(v)}</span>
          </span>
        );
      })}
      <span class="tip-note">
        {(() => {
          const known = hover.h === null ? knownDay(hover.d) : slot[hover.d][hover.h] === "up" || slot[hover.d][hover.h] === "down" ? 1 : 0;
          if (!known) return hover.h !== null && slot[hover.d][hover.h] === "future" ? "Still to come" : "No data from before the history began";
          return `Receiver up ${Math.round((100 * (hover.h === null ? upDay(hover.d) : up[hover.d][hover.h])) / known)} %`;
        })()}
      </span>
      <span class="tip-note">Click for the day's transmissions</span>
    </Tooltip>
  );

  return (
    <div class="chart" ref={ref} style={{ height: `${H}px` }}>
      {width > 0 && (
        <svg
          width={width}
          height={H}
          role="img"
          aria-label="Per day, and per hour of each day"
          onMouseMove={(e) => setHover(at(e))}
          onMouseLeave={() => setHover(null)}
          onClick={(e) => {
            const p = at(e);
            if (p) onDay(data.days[p.d]);
          }}
        >
          <defs>
            <pattern id="down-hatch" width="4" height="4" patternUnits="userSpaceOnUse" patternTransform="rotate(45)">
              <line x1="0" y1="0" x2="0" y2="4" class="hatch" />
            </pattern>
          </defs>
          {Array.from({ length: Math.round(top / step) + 1 }, (_, i) => i * step).map((v) => (
            <g key={v}>
              <line class="grid" x1={m.l} x2={m.l + iw} y1={yb(v)} y2={yb(v)} />
              <text class="axis" x={m.l - 6} y={yb(v) + 4} text-anchor="end">
                {axisFmt(v, step)}
              </text>
            </g>
          ))}
          {data.days.map((d, di) => {
            let acc = 0;
            const x0 = m.l + di * band + (band - bw) / 2;
            return (
              <g key={d} class={d === day ? "day selected" : "day"}>
                {d === day && <rect class="day-mark" x={m.l + di * band} y={m.t} width={band} height={BAR_H + m.gap + HEAT_H} />}
                {sums[di].map((v, i) => {
                  if (v <= 0) return null;
                  const y0 = yb(acc + v);
                  const hgt = yb(acc) - y0;
                  acc += v;
                  return <rect key={i} class="bar-seg" x={x0} y={y0} width={bw} height={Math.max(0, hgt - (hgt > 3 ? 1 : 0))} style={{ fill: seriesColor(i) }} />;
                })}
                {Array.from({ length: 24 }, (_, h) => {
                  const v = data.frequencies.reduce((s, f) => s + values[f.freq][di][h], 0);
                  const st = slot[di][h];
                  if (st === "nodata" || st === "future") return null;
                  const down = st === "down";
                  return (
                    <rect
                      key={h}
                      x={x0}
                      y={heatTop + h * cell}
                      width={bw}
                      height={cell - (cell > 5 ? 1 : 0)}
                      class={down ? "heat-down" : v === 0 ? "heat-zero" : "heat"}
                      fill={down ? "url(#down-hatch)" : undefined}
                      style={down || v === 0 ? undefined : { fill: `color-mix(in oklab, var(--heat-max) ${Math.round(15 + 85 * Math.sqrt(v / heatMax))}%, var(--heat-min))` }}
                    />
                  );
                })}
              </g>
            );
          })}
          {[0, 6, 12, 18].map((h) => (
            <text key={h} class="axis" x={m.l - 6} y={heatTop + h * cell + cell} text-anchor="end">
              {hh(h)}
            </text>
          ))}
          {data.days.map((d, di) =>
            (n - 1 - di) % labelEvery === 0 ? (
              <text key={d} class="axis" x={m.l + (di + 0.5) * band} y={H - 6} text-anchor="middle">
                {n <= 7 ? dayFmt.format(localDate(d)) : `${localDate(d).getDate()}/${localDate(d).getMonth() + 1}`}
              </text>
            ) : null,
          )}
          {hover && <rect class="hover-mark" x={m.l + hover.d * band} y={m.t} width={band} height={BAR_H + m.gap + HEAT_H} />}
        </svg>
      )}
      {tip}
    </div>
  );
}

function shiftDay(d: string, by: number): string {
  const t = localDate(d);
  t.setDate(t.getDate() + by);
  return `${t.getFullYear()}-${String(t.getMonth() + 1).padStart(2, "0")}-${String(t.getDate()).padStart(2, "0")}`;
}

function DayList({ day, onDay, frequencies }: { day: string; onDay: (d: string) => void; frequencies: Frequency[] }) {
  const [txs, setTxs] = useState<Transmission[] | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [only, setOnly] = useState<string | null>(null);
  const [missing, setMissing] = useState<Set<string>>(new Set());
  const [all, setAll] = useState(false);
  useEffect(() => setAll(false), [day, only]);
  const player = usePlayer();

  useEffect(() => {
    const ctl = new AbortController();
    setLoading(true);
    fetch(`/api/activity/transmissions?date=${day}&tz=${encodeURIComponent(tz)}`, { signal: ctl.signal })
      .then((r) => (r.ok ? r.json() : r.text().then((t) => Promise.reject(new Error(t.trim() || r.statusText)))))
      .then((d: Transmission[]) => {
        setTxs(d);
        setError(null);
        setLoading(false);
      })
      .catch((e) => {
        if (ctl.signal.aborted) return;
        setError(String(e.message ?? e));
        setLoading(false);
      });
    return () => ctl.abort();
  }, [day]);

  const play = async (t: Transmission, key: string) => {
    // the hub notes a transmission within a second of its end, so start a moment early
    const from = new Date(new Date(t.start).getTime() - 1000).toISOString();
    const r = await fetch(`/api/recordings/at?time=${encodeURIComponent(from)}`);
    if (!r.ok) {
      setMissing((m) => new Set(m).add(key));
      return;
    }
    const { recording, offset } = (await r.json()) as { recording: Recording; offset: number };
    player.play(recording, await loadWave(recording), Math.max(0, offset - LEAD_IN));
  };

  const index = new Map(frequencies.map((f, i) => [f.freq, i]));
  const shown = txs?.filter((t) => !only || t.freq === only) ?? null;
  const isToday = day >= today();

  return (
    <>
      <div class="day-list-head">
        <h3 id="day-list-title">{longDayFmt.format(localDate(day))}</h3>
        <div class="day-nav">
          {/* before the arrows, so they stay put when it appears */}
          {!isToday && (
            <button type="button" class="today" onClick={() => onDay(today())}>
              Today
            </button>
          )}
          <button type="button" onClick={() => onDay(shiftDay(day, -1))} title="Previous day">
            <span aria-hidden="true">‹</span>
            <span class="visually-hidden">Previous day</span>
          </button>
          <button type="button" onClick={() => onDay(shiftDay(day, 1))} disabled={isToday} title="Next day">
            <span aria-hidden="true">›</span>
            <span class="visually-hidden">Next day</span>
          </button>
        </div>
      </div>
      {frequencies.length > 1 && (
        <div class="freq-filter" role="group" aria-label="Frequency">
          <button type="button" aria-pressed={only === null} onClick={() => setOnly(null)}>
            All
          </button>
          {frequencies.map((f, i) => (
            <button key={f.freq} type="button" aria-pressed={only === f.freq} onClick={() => setOnly(only === f.freq ? null : f.freq)}>
              <span class="swatch" style={{ background: seriesColor(i) }} aria-hidden="true" />
              {f.freq}
            </button>
          ))}
        </div>
      )}
      {error && <p class="empty">Cannot list the transmissions: {error}</p>}
      {shown === null && !error && <p class="empty">Loading…</p>}
      {shown?.length === 0 && <p class="empty">No transmissions{only ? ` on ${only}` : ""} this day.</p>}
      {shown && shown.length > 0 && (
        <>
          <p class="secondary day-summary">
            {shown.length} transmissions, {seconds(shown.reduce((s, t) => s + t.duration_seconds, 0))} on air
          </p>
          <table class={loading ? "tx-table stale" : "tx-table"} aria-busy={loading}>
            <thead>
              <tr>
                <th scope="col">
                  <span class="visually-hidden">Play</span>
                </th>
                <th scope="col">Started</th>
                <th scope="col">Frequency</th>
                <th scope="col" class="num">
                  Length
                </th>
                <th scope="col" class="num">
                  SNR
                </th>
              </tr>
            </thead>
            <tbody>
              {(all ? shown : shown.slice(0, LIST_LIMIT)).map((t, i) => {
                const key = `${t.start}-${t.freq}-${i}`;
                return (
                  <tr key={key}>
                    <td>
                      {missing.has(key) ? (
                        <span class="no-rec" title="The recording of this moment is not there">
                          –
                        </span>
                      ) : (
                        <button type="button" class="tx-play" onClick={() => play(t, key)} title="Play from the recording">
                          <svg viewBox="0 0 16 16" aria-hidden="true">
                            <path d="M5 3.5v9l7.5-4.5z" />
                          </svg>
                          <span class="visually-hidden">Play {clock(t.start)}</span>
                        </button>
                      )}
                    </td>
                    <td>{clock(t.start)}</td>
                    <td>
                      <span class="swatch" style={{ background: seriesColor(index.get(t.freq) ?? 0) }} aria-hidden="true" />
                      <span class="mhz-small">{t.freq}</span> {stationName(t.label, t.freq)}
                    </td>
                    <td class="num">
                      {t.approximate && "≈ "}
                      {seconds(t.duration_seconds)}
                    </td>
                    <td class="num">{snr(t.snr_min_db, t.snr_max_db)}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
          {!all && shown.length > LIST_LIMIT && (
            <button type="button" class="show-all" onClick={() => setAll(true)}>
              Show all {shown.length}
            </button>
          )}
        </>
      )}
    </>
  );
}
