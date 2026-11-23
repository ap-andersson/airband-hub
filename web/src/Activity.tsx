// The Activity page: every stored transmission, one day at a time (up to 90 days back), each
// playable from the recordings. The day is in the address (?date=YYYY-MM-DD), so it can be linked.
import { useEffect, useState } from "preact/hooks";
import type { Recording, Transmission } from "./api";
import { clock, seconds, snr, stationName } from "./format";
import { LIST_LIMIT, localDate, longDayFmt, seriesColor, today, tz, useActivityData, type Frequency } from "./history";
import { LEAD_IN, usePlayer } from "./player";
import { loadWave } from "./Recordings";
import { navigate } from "./router";

export function Activity({ enabled, url }: { enabled: boolean; url: URL }) {
  const day = url.searchParams.get("date") ?? today();
  // the frequency list and their colours, the same as on the Stats page
  const freqs = useActivityData(enabled ? 1 : null).data?.frequencies ?? [];
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
      <h2>Activity</h2>
      <section class="panel day-list" aria-labelledby="day-list-title">
        {/* replace: stepping through days should not fill the back button's history */}
        <DayList day={day} onDay={(d) => navigate(`/activity?date=${d}`, true)} frequencies={freqs} />
      </section>
    </main>
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
