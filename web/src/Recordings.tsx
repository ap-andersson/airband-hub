import { useCallback, useEffect, useMemo, useRef, useState } from "preact/hooks";
import type { Day, Recording, Segment, Waveform } from "./api";
import { seconds } from "./format";
import { Link, navigate } from "./router";
import { decode, Wave, type Decoded } from "./Wave";

const tz = Intl.DateTimeFormat().resolvedOptions().timeZone;
const DETAIL_SPAN = 300; // seconds shown in the detail view
const LEAD_IN = 0.3; // start a transmission slightly early so its first word is not clipped

const clockFmt = new Intl.DateTimeFormat([], { hour: "2-digit", minute: "2-digit", second: "2-digit", hourCycle: "h23" });
const hourFmt = new Intl.DateTimeFormat([], { hour: "2-digit", minute: "2-digit", hourCycle: "h23" });
const dayFmt = new Intl.DateTimeFormat([], { weekday: "long", day: "numeric", month: "long", year: "numeric" });
const shortDayFmt = new Intl.DateTimeFormat([], { weekday: "short", day: "numeric" });
const monthFmt = new Intl.DateTimeFormat([], { month: "long", year: "numeric" });

const at = (rec: Recording, t: number) => new Date(new Date(rec.start).getTime() + t * 1000);
const localDate = (d: string) => new Date(`${d}T12:00:00`); // noon avoids DST edges

function useJSON<T>(url: string | null, refresh = 0): { data: T | null; error: string | null } {
  const [state, setState] = useState<{ data: T | null; error: string | null }>({ data: null, error: null });
  useEffect(() => {
    if (!url) return;
    const ctl = new AbortController();
    fetch(url, { signal: ctl.signal })
      .then((r) => (r.ok ? r.json() : r.text().then((t) => Promise.reject(new Error(t.trim() || r.statusText)))))
      .then((data) => setState({ data, error: null }))
      .catch((e) => !ctl.signal.aborted && setState({ data: null, error: String(e.message ?? e) }));
    return () => ctl.abort();
  }, [url, refresh]);
  return state;
}

// Finished recordings never change, so their waveforms are kept for the whole visit.
const waveCache = new Map<string, Promise<Decoded>>();

function loadWave(rec: Recording): Promise<Decoded> {
  const url = `/api/recordings/waveform?path=${encodeURIComponent(rec.path)}`;
  const fetchIt = () =>
    fetch(url)
      .then((r) => (r.ok ? (r.json() as Promise<Waveform>) : Promise.reject(new Error(r.statusText))))
      .then(decode);
  if (rec.growing) return fetchIt();
  let p = waveCache.get(rec.path);
  if (!p) {
    p = fetchIt();
    p.catch(() => waveCache.delete(rec.path));
    waveCache.set(rec.path, p);
  }
  return p;
}

interface PlayerState {
  rec: Recording | null;
  wave: Decoded | null;
  playing: boolean;
  position: number;
}

function usePlayer() {
  const audio = useRef<HTMLAudioElement>(new Audio());
  const offset = useRef(0); // the recording time the current audio URL starts at
  const [state, setState] = useState<PlayerState>({ rec: null, wave: null, playing: false, position: 0 });
  const [skipSilence, setSkipSilence] = useState(false);
  const live = useRef({ state, skipSilence });
  live.current = { state, skipSilence };

  const play = useCallback((rec: Recording, wave: Decoded, t: number) => {
    const el = audio.current;
    offset.current = Math.max(0, t);
    el.src = `/api/recordings/audio?path=${encodeURIComponent(rec.path)}&from=${offset.current.toFixed(3)}`;
    setState({ rec, wave, playing: true, position: offset.current });
    el.play().catch(() => setState((s) => ({ ...s, playing: false })));
  }, []);

  const toggle = useCallback(() => {
    const el = audio.current;
    if (!live.current.state.rec) return;
    if (el.paused) {
      el.play().then(() => setState((s) => ({ ...s, playing: true })));
    } else {
      el.pause();
      setState((s) => ({ ...s, playing: false }));
    }
  }, []);

  const jump = useCallback(
    (dir: 1 | -1) => {
      const { rec, wave, position } = live.current.state;
      if (!rec || !wave) return;
      const tx = wave.transmissions;
      const target =
        dir > 0
          ? tx.find((s) => s.start - LEAD_IN > position + 0.5)
          : [...tx].reverse().find((s) => s.start - LEAD_IN < position - 1.5);
      if (target) play(rec, wave, target.start - LEAD_IN);
    },
    [play],
  );

  useEffect(() => {
    const el = audio.current;
    let frame = 0;
    const tick = () => {
      const { state: s, skipSilence: skip } = live.current;
      if (s.rec && s.wave && !el.paused) {
        const pos = offset.current + el.currentTime;
        if (skip) {
          const inside = s.wave.transmissions.some((seg) => pos >= seg.start - LEAD_IN - 0.2 && pos <= seg.end + 0.5);
          if (!inside) {
            const next = s.wave.transmissions.find((seg) => seg.start - LEAD_IN > pos);
            if (next) {
              play(s.rec, s.wave, next.start - LEAD_IN);
            } else {
              el.pause();
              setState((st) => ({ ...st, playing: false }));
            }
            frame = requestAnimationFrame(tick);
            return;
          }
        }
        setState((st) => (Math.abs(st.position - pos) > 0.04 ? { ...st, position: pos } : st));
      }
      frame = requestAnimationFrame(tick);
    };
    frame = requestAnimationFrame(tick);
    const ended = () => setState((s) => ({ ...s, playing: false }));
    el.addEventListener("ended", ended);
    return () => {
      cancelAnimationFrame(frame);
      el.removeEventListener("ended", ended);
      el.pause();
    };
  }, [play]);

  return { state, play, toggle, jump, skipSilence, setSkipSilence };
}

type Player = ReturnType<typeof usePlayer>;

export function Recordings({ enabled, url }: { enabled: boolean; url: URL }) {
  const days = useJSON<Day[]>(enabled ? `/api/recordings/days?tz=${encodeURIComponent(tz)}` : null);
  const date = url.searchParams.get("date") ?? days.data?.[0]?.date ?? null;
  const [refresh, setRefresh] = useState(0);
  const recs = useJSON<Recording[]>(date ? `/api/recordings?date=${date}&tz=${encodeURIComponent(tz)}` : null, refresh);
  const player = usePlayer();
  const [open, setOpen] = useState<{ path: string; focus: number } | null>(null);

  // the day being recorded right now gains files and grows; look again every minute
  useEffect(() => {
    if (!recs.data?.some((r) => r.growing)) return;
    const id = setInterval(() => setRefresh((n) => n + 1), 60_000);
    return () => clearInterval(id);
  }, [recs.data]);

  if (!enabled) {
    return (
      <main>
        <p class="empty">
          Set <code>HUB_RECORDINGS_DIR</code> to the folder rtl_airband records to, and the recordings show up here.
        </p>
      </main>
    );
  }

  return (
    <main class="recordings">
      <DayList days={days.data} error={days.error} selected={date} />
      <section class="day" aria-labelledby="day-title">
        {date && <h2 id="day-title">{dayFmt.format(localDate(date))}</h2>}
        {recs.error && <p class="empty">Cannot list the recordings: {recs.error}</p>}
        {days.data?.length === 0 && <p class="empty">No recordings yet. They appear here once rtl_airband's file output has written one.</p>}
        {recs.data?.length === 0 && <p class="empty">No recordings start on this day.</p>}
        <ol class="hours">
          {recs.data?.map((rec) => (
            <HourRow
              key={rec.path}
              rec={rec}
              player={player}
              open={open?.path === rec.path ? open.focus : null}
              onOpen={(focus) => setOpen({ path: rec.path, focus })}
            />
          ))}
        </ol>
      </section>
      <PlayerBar player={player} />
    </main>
  );
}

function DayList({ days, error, selected }: { days: Day[] | null; error: string | null; selected: string | null }) {
  const months = useMemo(() => {
    const out: { month: string; days: Day[] }[] = [];
    for (const d of days ?? []) {
      const m = monthFmt.format(localDate(d.date));
      if (out.at(-1)?.month !== m) out.push({ month: m, days: [] });
      out.at(-1)!.days.push(d);
    }
    return out;
  }, [days]);

  return (
    <nav class="days" aria-label="Days">
      {error && <p class="empty">Cannot list days: {error}</p>}
      {days === null && !error && <p class="empty">Loading days…</p>}
      {months.map((m) => (
        <div key={m.month} class="month">
          <h3>{m.month}</h3>
          <ul>
            {m.days.map((d) => (
              <li key={d.date}>
                <Link href={`/recordings?date=${d.date}`} current={d.date === selected} class="day-link">
                  {shortDayFmt.format(localDate(d.date))}
                </Link>
              </li>
            ))}
          </ul>
        </div>
      ))}
    </nav>
  );
}

function HourRow({ rec, player, open, onOpen }: { rec: Recording; player: Player; open: number | null; onOpen: (focus: number) => void }) {
  const row = useRef<HTMLLIElement>(null);
  const [visible, setVisible] = useState(false);
  const [wave, setWave] = useState<Decoded | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    const el = row.current;
    if (!el) return;
    const io = new IntersectionObserver(([e]) => e.isIntersecting && setVisible(true), { rootMargin: "300px" });
    io.observe(el);
    return () => io.disconnect();
  }, []);

  useEffect(() => {
    if (!visible) return;
    let cancelled = false;
    loadWave(rec).then(
      (w) => !cancelled && setWave(w),
      (e) => !cancelled && setError(String(e.message ?? e)),
    );
    return () => {
      cancelled = true;
    };
  }, [visible, rec.path, rec.size]);

  const playing = player.state.rec?.path === rec.path;
  const playhead = playing ? player.state.position : null;
  const start = new Date(rec.start);
  const end = wave ? new Date(Math.round(at(rec, wave.duration).getTime() / 60_000) * 60_000) : null;
  const airtime = wave?.transmissions.reduce((sum, s) => sum + s.end - s.start, 0) ?? 0;
  const focus = open ?? (playing ? player.state.position : null);

  const pick = (t: number, snapped: Segment | null) => {
    if (!wave) return;
    const from = snapped ? snapped.start - LEAD_IN : t;
    onOpen(snapped ? snapped.start : t);
    player.play(rec, wave, from);
  };

  // keep the detail window still while the playhead is inside it; move on when it leaves
  const [detailFrom, setDetailFrom] = useState<number | null>(null);
  useEffect(() => {
    if (focus === null || !wave) return;
    setDetailFrom((cur) => {
      if (cur !== null && focus >= cur + 10 && focus <= cur + DETAIL_SPAN - 10) return cur;
      return Math.max(0, Math.min(Math.max(wave.duration - DETAIL_SPAN, 0), focus - DETAIL_SPAN / 3));
    });
  }, [focus === null ? null : Math.floor(focus / 5), wave]);

  const name = rec.freq ? `${rec.name} ${rec.freq}` : rec.name;
  return (
    <li ref={row} class={`hour${playing ? " current" : ""}${wave && wave.transmissions.length === 0 ? " quiet" : ""}`}>
      <div class="hour-head">
        <h3>
          {hourFmt.format(start)}
          {end && `–${hourFmt.format(end)}`}
        </h3>
        <span class="hour-meta">
          {rec.growing && <span class="recording-now">Recording now</span>}
          {wave ? (wave.transmissions.length === 0 ? "No transmissions" : `${wave.transmissions.length === 1 ? "1 transmission" : `${wave.transmissions.length} transmissions`}, ${seconds(airtime)}`) : error ? `Cannot read: ${error}` : "Reading…"}
        </span>
        <a class="download" href={`/api/recordings/download?path=${encodeURIComponent(rec.path)}`} download title={`Download ${name}`}>
          Download
        </a>
      </div>
      {wave && wave.transmissions.length === 0 && open === null ? null : wave ? (
        <Wave
          wave={wave}
          from={0}
          to={Math.max(wave.duration, 1)}
          playhead={playhead}
          height={44}
          label={(t) => clockFmt.format(at(rec, t))}
          onPick={pick}
          ariaLabel={`Waveform of ${name} from ${hourFmt.format(start)}, ${wave.transmissions.length} transmissions`}
        />
      ) : (
        <div class="wave placeholder" style={{ height: "44px" }} />
      )}
      {wave && open !== null && detailFrom !== null && (
        <div class="detail">
          <Wave
            wave={wave}
            from={detailFrom}
            to={detailFrom + DETAIL_SPAN}
            playhead={playhead}
            height={96}
            label={(t) => clockFmt.format(at(rec, t))}
            onPick={(t) => {
              onOpen(t);
              player.play(rec, wave, t);
            }}
            ariaLabel={`Detail from ${clockFmt.format(at(rec, detailFrom))}`}
          />
          <div class="detail-axis" aria-hidden="true">
            <span>{clockFmt.format(at(rec, detailFrom))}</span>
            <span>{clockFmt.format(at(rec, detailFrom + DETAIL_SPAN))}</span>
          </div>
          {wave.transmissions.length > 0 && (
            <ul class="tx-list" aria-label="Transmissions in this hour">
              {wave.transmissions.map((s) => {
                const current = playhead !== null && playhead >= s.start - LEAD_IN && playhead <= s.end;
                return (
                  <li key={s.start}>
                    <button
                      type="button"
                      class={current ? "tx current" : "tx"}
                      onClick={() => {
                        onOpen(s.start);
                        player.play(rec, wave, s.start - LEAD_IN);
                      }}
                    >
                      <span class="tx-time">{clockFmt.format(at(rec, s.start))}</span>
                      <span class="tx-len">{seconds(s.end - s.start)}</span>
                    </button>
                  </li>
                );
              })}
            </ul>
          )}
        </div>
      )}
    </li>
  );
}

function PlayerBar({ player }: { player: Player }) {
  const { state, toggle, jump, skipSilence, setSkipSilence } = player;
  useEffect(() => {
    const key = (e: KeyboardEvent) => {
      if (e.target instanceof HTMLInputElement || e.target instanceof HTMLButtonElement || e.target instanceof HTMLAnchorElement) return;
      if (e.key === " ") {
        e.preventDefault();
        toggle();
      } else if (e.key === "ArrowRight" && e.altKey) jump(1);
      else if (e.key === "ArrowLeft" && e.altKey) jump(-1);
    };
    addEventListener("keydown", key);
    return () => removeEventListener("keydown", key);
  }, [toggle, jump]);

  if (!state.rec) {
    return (
      <div class="player idle" role="region" aria-label="Player">
        Click a waveform or a transmission to listen.
      </div>
    );
  }
  const when = at(state.rec, state.position);
  return (
    <div class="player" role="region" aria-label="Player">
      <div class="player-buttons">
        <button type="button" onClick={() => jump(-1)} title="Previous transmission (Alt+Left)">
          <svg viewBox="0 0 16 16" aria-hidden="true">
            <path d="M4 3v10M13 3.5v9L6 8z" />
          </svg>
          <span class="visually-hidden">Previous transmission</span>
        </button>
        <button type="button" class="play" onClick={toggle} title="Play or pause (Space)">
          <svg viewBox="0 0 16 16" aria-hidden="true">
            {state.playing ? <path d="M4.5 3h2.5v10H4.5zM9 3h2.5v10H9z" /> : <path d="M5 3.5v9l7.5-4.5z" />}
          </svg>
          <span class="visually-hidden">{state.playing ? "Pause" : "Play"}</span>
        </button>
        <button type="button" onClick={() => jump(1)} title="Next transmission (Alt+Right)">
          <svg viewBox="0 0 16 16" aria-hidden="true">
            <path d="M12 3v10M3 3.5v9L10 8z" />
          </svg>
          <span class="visually-hidden">Next transmission</span>
        </button>
      </div>
      <div class="player-where">
        <strong>{clockFmt.format(when)}</strong>
        <span class="secondary">{dayFmt.format(when)}</span>
      </div>
      <label class="skip">
        <input type="checkbox" checked={skipSilence} onChange={(e) => setSkipSilence(e.currentTarget.checked)} />
        Skip silence
      </label>
      <button
        type="button"
        class="player-day"
        onClick={() => {
          const d = new Date(state.rec!.start);
          const date = `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
          navigate(`/recordings?date=${date}`);
        }}
      >
        Show its day
      </button>
    </div>
  );
}
