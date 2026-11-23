// The Stats page: the last 30 days summed up per frequency (tiles, ranked boxes, the average
// day), and transmissions or airtime per day over 7, 30 or 90 days with a day × hour heatmap.
// Clicking a day opens it on the Activity page.
import { useMemo, useState } from "preact/hooks";
import { seconds, stationName } from "./format";
import {
  dayFmt,
  hh,
  Legend,
  localDate,
  niceScale,
  RANGES,
  seriesColor,
  stored,
  Tooltip,
  upShare,
  useActivityData,
  useWidth,
  type ActivityData,
  type Hours,
  type Measure,
  type Slot,
} from "./history";
import { navigate } from "./router";

const SUMMARY_DAYS = 30;

const measureFmt = (measure: Measure) => (v: number) => (measure === "count" ? (v >= 10 ? v.toFixed(0) : v.toFixed(1)) : seconds(v));
// axis ticks: whole numbers when the step allows it
const measureAxisFmt = (measure: Measure) => (v: number, step: number) => (measure === "count" ? v.toFixed(step >= 1 ? 0 : 1) : seconds(v));

function MeasureToggle({ measure, onChange }: { measure: Measure; onChange: (m: Measure) => void }) {
  return (
    <div class="segmented" role="group" aria-label="Measure">
      <button type="button" aria-pressed={measure === "count"} onClick={() => onChange("count")}>
        Transmissions
      </button>
      <button type="button" aria-pressed={measure === "airtime"} onClick={() => onChange("airtime")}>
        Airtime
      </button>
    </div>
  );
}

export function Stats({ enabled }: { enabled: boolean }) {
  const [range, setRange] = stored<string>("hub.statsRange", RANGES.map(String), "30");
  const summary = useActivityData(enabled ? SUMMARY_DAYS : null);
  const perDay = useActivityData(enabled ? Number(range) : null);

  if (!enabled) {
    return (
      <main>
        <p class="empty">
          Stats need rtl_airband's metrics (<code>HUB_METRICS_URL</code>) and a data folder (<code>HUB_DATA_DIR</code>).
        </p>
      </main>
    );
  }
  const error = summary.error ?? perDay.error;
  const data = summary.data;
  return (
    <main class="activity">
      <h2>Stats</h2>
      {error && <p class="empty">Cannot read the history: {error}</p>}
      {!data && !error && <p class="empty">Loading…</p>}
      {data && data.frequencies.length === 0 && <p class="empty">No transmissions recorded yet. They are kept for 90 days from when the hub first sees them.</p>}
      {data && data.frequencies.length > 0 && (
        <>
          <Summary data={data} />
          <AverageDay data={data} />
          <PerDay data={perDay.data} range={range} onRange={setRange} />
        </>
      )}
    </main>
  );
}

function Summary({ data }: { data: ActivityData }) {
  const { share: up, slot } = useMemo(() => upShare(data), [data]);
  const total = (m: Record<string, Hours[]>) => data.frequencies.reduce((s, f) => s + m[f.freq].flat().reduce((a, b) => a + b, 0), 0);
  const upHours = up.flat().reduce((a, b) => a + b, 0);
  const knownHours = slot.flat().filter((s) => s === "up" || s === "down").length;

  // per frequency: transmissions per day the receiver was up, and their mean length
  const perFreq = data.frequencies.map((f, i) => {
    const count = data.counts[f.freq].flat().reduce((a, b) => a + b, 0);
    const airtime = data.airtime[f.freq].flat().reduce((a, b) => a + b, 0);
    return {
      freq: f.freq,
      name: stationName(f.label, f.freq),
      color: seriesColor(i),
      perDay: upHours > 0 ? count / (upHours / 24) : 0,
      length: count > 0 ? airtime / count : 0,
    };
  });

  return (
    <>
      <div class="stat-tiles" aria-label={`The last ${SUMMARY_DAYS} days`}>
        <div class="stat">
          <span class="stat-value">{total(data.counts).toLocaleString()}</span>
          <span class="stat-label">transmissions, last {SUMMARY_DAYS} days</span>
        </div>
        <div class="stat">
          <span class="stat-value">{seconds(total(data.airtime))}</span>
          <span class="stat-label">on air, last {SUMMARY_DAYS} days</span>
        </div>
        <div class="stat">
          <span class="stat-value">{knownHours ? `${Math.round((100 * upHours) / knownHours)} %` : "–"}</span>
          <span class="stat-label">receiver up, last {SUMMARY_DAYS} days</span>
        </div>
      </div>
      <div class="toplists">
        <TopList
          title={`Transmissions per day, average over the last ${SUMMARY_DAYS} days`}
          rows={perFreq.map((r) => ({ ...r, value: r.perDay }))}
          fmt={(v) => (v >= 10 ? Math.round(v).toLocaleString() : v.toFixed(1))}
          note="Per day the receiver was up"
        />
        <TopList title={`Transmission length, average over the last ${SUMMARY_DAYS} days`} rows={perFreq.map((r) => ({ ...r, value: r.length }))} fmt={(v) => seconds(v)} />
      </div>
    </>
  );
}

function AverageDay({ data }: { data: ActivityData }) {
  const [measure, setMeasure] = stored<Measure>("hub.statsAvgMeasure", ["count", "airtime"], "count");
  const { share: up } = useMemo(() => upShare(data), [data]);
  const values = measure === "count" ? data.counts : data.airtime;
  const fmt = measureFmt(measure);

  // per hour of day: the sum over the period divided by the hours the receiver was up
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
    <section class="panel chart-panel" aria-labelledby="avg-title">
      <div class="chart-head">
        <h3 id="avg-title">
          {measure === "count" ? "Transmissions" : "Airtime"} per hour, average day over the last {SUMMARY_DAYS} days
        </h3>
        <MeasureToggle measure={measure} onChange={setMeasure} />
      </div>
      <Legend frequencies={data.frequencies} />
      <HourChart series={average} fmt={fmt} axisFmt={measureAxisFmt(measure)} />
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
  );
}

function PerDay({ data, range, onRange }: { data: ActivityData | null; range: string; onRange: (r: string) => void }) {
  const [measure, setMeasure] = stored<Measure>("hub.statsMeasure", ["count", "airtime"], "count");
  const [only, setOnly] = useState<string | null>(null);
  const shares = useMemo(() => (data ? upShare(data) : null), [data]);
  return (
    <section class="panel chart-panel" aria-labelledby="days-title">
      <div class="chart-head">
        <h3 id="days-title">
          {measure === "count" ? "Transmissions" : "Airtime"} per day, last {range} days
        </h3>
        <div class="activity-controls">
          <MeasureToggle measure={measure} onChange={setMeasure} />
          <div class="segmented" role="group" aria-label="Period">
            {RANGES.map((r) => (
              <button key={r} type="button" aria-pressed={range === String(r)} onClick={() => onRange(String(r))}>
                {r} days
              </button>
            ))}
          </div>
        </div>
      </div>
      {!data || !shares ? (
        <p class="empty">Loading…</p>
      ) : (
        <>
          <Legend frequencies={data.frequencies} only={only} onOnly={setOnly} />
          <DayChart
            data={data}
            values={measure === "count" ? data.counts : data.airtime}
            up={shares.share}
            slot={shares.slot}
            fmt={measureFmt(measure)}
            axisFmt={measureAxisFmt(measure)}
            day={null}
            onDay={(d) => navigate(`/activity?date=${d}`)}
            only={only}
          />
          <p class="chart-note">
            Below the bars: each hour of each day, darker for more. <span class="hatch-key" aria-hidden="true" /> the receiver was down. Click a day to see its
            transmissions.
          </p>
        </>
      )}
    </section>
  );
}

interface TopRow {
  freq: string;
  name: string;
  color: string;
  value: number;
}

// One value per frequency, highest first, each with a bar against the highest.
function TopList({ title, rows, fmt, note }: { title: string; rows: TopRow[]; fmt: (v: number) => string; note?: string }) {
  const sorted = [...rows].sort((a, b) => b.value - a.value);
  const max = Math.max(...rows.map((r) => r.value), 0);
  return (
    <section class="panel toplist">
      <h3>{title}</h3>
      <ol>
        {sorted.map((r) => (
          <li key={r.freq}>
            <span class="toplist-name">
              <span class="swatch" style={{ background: r.color }} aria-hidden="true" />
              <span class="mhz-small">{r.freq}</span> {r.name}
            </span>
            <span class="toplist-value">{fmt(r.value)}</span>
            <span class="toplist-bar" aria-hidden="true">
              <span style={{ width: `${max > 0 ? (100 * r.value) / max : 0}%`, background: r.color }} />
            </span>
          </li>
        ))}
      </ol>
      {note && <p class="chart-note">{note}</p>}
    </section>
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
  only,
}: {
  data: ActivityData;
  values: Record<string, Hours[]>;
  up: Hours[];
  slot: Slot[][];
  only: string | null;
  fmt: (v: number) => string;
  axisFmt: (v: number, step: number) => string;
  day: string | null; // highlighted, if any
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
  // with one frequency picked, the others count as zero in the bars and the heatmap
  const shown = data.frequencies.map((f) => only === null || f.freq === only);
  const sums = data.days.map((_, d) => data.frequencies.map((f, i) => (shown[i] ? values[f.freq][d].reduce((a, b) => a + b, 0) : 0)));
  const hourSum = (d: number, h: number) => data.frequencies.reduce((s, f, i) => s + (shown[i] ? values[f.freq][d][h] : 0), 0);
  const { top, step } = niceScale(Math.max(...sums.map((s) => s.reduce((a, b) => a + b, 0))));
  const yb = (v: number) => m.t + BAR_H - (v / top) * BAR_H;
  const heatTop = m.t + BAR_H + m.gap;
  const cell = HEAT_H / 24;
  const heatMax = Math.max(1, ...data.days.flatMap((_, d) => Array.from({ length: 24 }, (_, h) => hourSum(d, h))));
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
        if (!shown[i]) return null;
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
                  const v = hourSum(di, h);
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
