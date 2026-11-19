import { Fragment, type ComponentChildren } from "preact";
import { useEffect, useState } from "preact/hooks";
import type { Frequency, Live, Mount, Receiver, Transmission } from "./api";
import { ago, clock, seconds, snr, stationName } from "./format";
import { LoginDialog, logout, useLoggedIn } from "./login";
import { PlayerBar, PlayerProvider, usePlayer } from "./player";
import { Recordings } from "./Recordings";
import { Link, useLocation } from "./router";
import { useLive, type Connection } from "./useLive";

export function App() {
  return (
    <PlayerProvider>
      <Shell />
    </PlayerProvider>
  );
}

function Shell() {
  const { live, connection } = useLive();
  const url = useLocation();
  const now = live ? new Date(live.time) : new Date();
  const mount = live?.mounts[0];
  const page = url.pathname.startsWith("/recordings") ? "recordings" : "live";

  const title = live?.title || mount?.info.name || "Airband hub";
  useEffect(() => {
    document.title = page === "recordings" ? `Recordings, ${title}` : title;
  }, [page, title]);

  return (
    <div class="page">
      <Header title={title} mount={mount} connection={connection} page={page} recordingsEnabled={!!live?.recordings_enabled} loggedIn={!!live?.logged_in} />
      <LoginDialog />
      {live === null ? (
        <p class="empty">{connection === "lost" ? "Cannot reach the hub. Retrying…" : "Connecting to the hub…"}</p>
      ) : page === "recordings" ? (
        <Recordings enabled={live.recordings_enabled} bookmarksEnabled={live.bookmarks_enabled} url={url} />
      ) : (
        <main>
          <Board live={live} />
          <div class="lower">
            <Recent recent={live.recent} metricsEnabled={live.metrics_enabled} now={now} />
            <ReceiverPanel receiver={live.receiver} metricsEnabled={live.metrics_enabled} />
          </div>
        </main>
      )}
      <PlayerBar
        hint={page === "recordings" && live?.recordings_enabled ? "Click a waveform or a transmission to listen." : null}
        bookmarksEnabled={!!live?.bookmarks_enabled}
        mount={mount}
      />
    </div>
  );
}

function Header({
  title,
  mount,
  connection,
  page,
  recordingsEnabled,
  loggedIn: hubSaysLoggedIn,
}: {
  title: string;
  mount?: Mount;
  connection: Connection;
  page: string;
  recordingsEnabled: boolean;
  loggedIn: boolean;
}) {
  const loggedIn = useLoggedIn(hubSaysLoggedIn);
  return (
    <header class="header">
      <h1>{title}</h1>
      <div class="menubar">
        <nav class="nav" aria-label="Pages">
        <Link href="/" current={page === "live"}>
          Live
        </Link>
        {recordingsEnabled && (
          <Link href="/recordings" current={page === "recordings"}>
            Recordings
          </Link>
        )}
        </nav>
        <div class="menubar-right">
          {loggedIn && (
            <button type="button" class="logout" onClick={logout}>
              Log out
            </button>
          )}
          <Listen mount={mount} connection={connection} />
        </div>
      </div>
    </header>
  );
}

function Listen({ mount, connection }: { mount?: Mount; connection: Connection }) {
  const player = usePlayer();
  const { state } = player;
  const live = !!mount?.source_connected && connection === "open";
  const on = !!state.live && (state.status === "playing" || state.status === "loading");
  const loading = !!state.live && state.status === "loading";

  let status: string;
  if (connection === "lost") status = "Hub unreachable";
  else if (!mount) status = "No stream from rtl_airband";
  else if (!mount.source_connected) status = "Stream interrupted, waiting for rtl_airband";
  else status = mount.listeners === 1 ? "1 listener" : `${mount.listeners} listeners`;

  return (
    <div class="listen">
      <span class={live ? "stream-status ok" : "stream-status bad"}>
        <StatusIcon ok={live} />
        {status}
      </span>
      <button
        type="button"
        class={`listen-button${on ? " playing" : ""}${loading ? " loading" : ""}`}
        onClick={() => (on ? player.stopLive() : mount && player.playLive(mount.mount))}
        disabled={!mount}
        aria-pressed={on}
        aria-busy={loading}
      >
        {loading ? (
          <span class="spinner" aria-hidden="true" />
        ) : (
          <svg viewBox="0 0 16 16" aria-hidden="true">
            {on ? <rect x="4" y="4" width="8" height="8" rx="1" /> : <path d="M5 3.5v9l7.5-4.5z" />}
          </svg>
        )}
        {loading ? "Connecting…" : on ? "Stop" : "Listen live"}
      </button>
    </div>
  );
}

function StatusIcon({ ok }: { ok: boolean }) {
  return ok ? (
    <svg class="status-icon" viewBox="0 0 16 16" aria-hidden="true">
      <circle cx="8" cy="8" r="7" />
      <path d="M4.8 8.2l2.1 2.1 4.3-4.6" fill="none" stroke-width="1.8" />
    </svg>
  ) : (
    <svg class="status-icon" viewBox="0 0 16 16" aria-hidden="true">
      <circle cx="8" cy="8" r="7" />
      <path d="M8 4.5v4.2M8 11.2v.3" fill="none" stroke-width="1.8" />
    </svg>
  );
}

// dBFS range drawn by the level meters
const METER_MIN = -60;
const METER_MAX = 0;
const pct = (dbfs: number) => (100 * (Math.min(METER_MAX, Math.max(METER_MIN, dbfs)) - METER_MIN)) / (METER_MAX - METER_MIN);

function Board({ live }: { live: Live }) {
  if (!live.metrics_enabled) {
    return (
      <section class="board">
        <h2>Frequencies</h2>
        <p class="empty">
          Set <code>HUB_METRICS_URL</code> to rtl_airband's metrics endpoint to see levels and activity per frequency.
        </p>
      </section>
    );
  }
  return (
    <section class="board" aria-labelledby="board-title">
      <div class="board-head">
        <h2 id="board-title">Frequencies</h2>
        <MeterKey />
      </div>
      {live.frequencies.length === 0 ? (
        <p class="empty">{live.receiver.ok ? "rtl_airband reports no frequencies." : "Waiting for rtl_airband's metrics."}</p>
      ) : (
        <ol class="strips">
          {live.frequencies.map((f) => (
            <Strip key={f.freq} f={f} stale={!live.receiver.ok} />
          ))}
        </ol>
      )}
    </section>
  );
}

function MeterKey() {
  return (
    <div class="meter-key" aria-hidden="true">
      <span>
        <i class="key-signal" />
        Signal
      </span>
      <span>
        <i class="key-noise" />
        Noise floor
      </span>
      <span>
        <i class="key-squelch" />
        Squelch opens
      </span>
      <span class="meter-scale">Scale {METER_MIN} to {METER_MAX} dBFS</span>
    </div>
  );
}

function Strip({ f, stale }: { f: Frequency; stale: boolean }) {
  const above = f.signal_dbfs - f.noise_dbfs;
  return (
    <li class={f.open ? "strip on-air" : "strip"} aria-label={`${f.freq} ${stationName(f.label, f.freq)}${f.open ? ", on air" : ""}`}>
      <div class="strip-freq">
        <span class="mhz">{f.freq}</span>
      </div>
      <div class="strip-main">
        <div class="strip-title">
          <span class="name">{stationName(f.label, f.freq)}</span>
          {f.open && (
            <span class="on-air-badge">
              <svg viewBox="0 0 16 16" aria-hidden="true">
                <circle cx="8" cy="8" r="2" />
                <path d="M4.5 4.5a5 5 0 000 7M11.5 4.5a5 5 0 010 7" fill="none" stroke-width="1.6" />
              </svg>
              On air
            </span>
          )}
        </div>
        <div
          class={stale ? "meter stale" : "meter"}
          role="img"
          aria-label={`Signal ${f.signal_dbfs.toFixed(1)} dBFS, noise floor ${f.noise_dbfs.toFixed(1)} dBFS, squelch opens at ${f.squelch_dbfs.toFixed(1)} dBFS`}
          title={`Signal ${f.signal_dbfs.toFixed(1)} dBFS (${above >= 0 ? "+" : ""}${above.toFixed(1)} dB over noise)\nNoise floor ${f.noise_dbfs.toFixed(1)} dBFS\nSquelch opens at ${f.squelch_dbfs.toFixed(1)} dBFS`}
        >
          <div class="meter-noise" style={{ width: `${pct(f.noise_dbfs)}%` }} />
          <div class="meter-signal" style={{ width: `${pct(f.signal_dbfs)}%` }} />
          <div class="meter-squelch" style={{ left: `${pct(f.squelch_dbfs)}%` }} />
        </div>
      </div>
      <dl class="strip-stats">
        <div>
          <dt>Signal</dt>
          <dd>{f.signal_dbfs.toFixed(1)} dBFS</dd>
        </div>
        <div>
          <dt>Last hour</dt>
          <dd>
            {f.tx_last_hour === 1 ? "1 transmission" : `${f.tx_last_hour} transmissions`}
            {f.tx_last_hour > 0 && <span class="secondary">, {seconds(f.airtime_last_hour_seconds)}</span>}
          </dd>
        </div>
      </dl>
    </li>
  );
}

const GROUP_GAP = 10; // seconds; closer transmissions on one frequency form a group

// recent is newest first; a group collects back-to-back transmissions on one frequency
function groupRecent(recent: Transmission[]): Transmission[][] {
  const groups: Transmission[][] = [];
  for (const t of recent) {
    const g = groups.at(-1);
    const older = g?.at(-1);
    if (g && older && older.freq === t.freq && (new Date(older.start).getTime() - new Date(t.end).getTime()) / 1000 < GROUP_GAP) g.push(t);
    else groups.push([t]);
  }
  return groups;
}

function TxRow({ t, now, sub }: { t: Transmission; now: Date; sub?: boolean }) {
  return (
    <tr class={sub ? "sub" : undefined}>
      <td title={ago(t.start, now)}>{clock(t.start)}</td>
      <td>
        <span class="mhz-small">{t.freq}</span> {stationName(t.label, t.freq)}
      </td>
      <td class="num">
        {t.approximate && <span title="Several ended within one second; this is their average">≈ </span>}
        {seconds(t.duration_seconds)}
      </td>
      <td class="num">{snr(t.snr_min_db, t.snr_max_db)}</td>
    </tr>
  );
}

function Recent({ recent, metricsEnabled, now }: { recent: Transmission[]; metricsEnabled: boolean; now: Date }) {
  // keyed by the group's first transmission, which stays put while newer ones join it
  const [open, setOpen] = useState<Set<string>>(new Set());
  const toggle = (key: string) =>
    setOpen((o) => {
      const n = new Set(o);
      if (!n.delete(key)) n.add(key);
      return n;
    });

  return (
    <Panel id="recent" title="Recent transmissions">
      {!metricsEnabled ? (
        <p class="empty">Needs rtl_airband's metrics.</p>
      ) : recent.length === 0 ? (
        <p class="empty">None since the hub started. They show up here as they end.</p>
      ) : (
        <table>
          <thead>
            <tr>
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
            {groupRecent(recent).map((g) => {
              const first = g.at(-1)!;
              const key = `${first.start}-${first.freq}`;
              if (g.length === 1) return <TxRow key={key} t={first} now={now} />;
              const isOpen = open.has(key);
              const snrs = g.filter((t) => t.snr_min_db !== 0 || t.snr_max_db !== 0);
              return (
                <Fragment key={key}>
                  <tr class={isOpen ? "group open" : "group"} onClick={() => toggle(key)}>
                    <td title={ago(first.start, now)}>
                      <button type="button" class="group-toggle" aria-expanded={isOpen}>
                        {clock(first.start)}
                      </button>
                    </td>
                    <td>
                      <span class="mhz-small">{first.freq}</span> {stationName(first.label, first.freq)}{" "}
                      <span class="group-count">{g.length} transmissions</span>
                    </td>
                    <td class="num" title="Total airtime">
                      {seconds(g.reduce((sum, t) => sum + t.duration_seconds, 0))}
                    </td>
                    <td class="num">
                      {snrs.length ? snr(Math.min(...snrs.map((t) => t.snr_min_db)), Math.max(...snrs.map((t) => t.snr_max_db))) : "–"}
                    </td>
                  </tr>
                  {isOpen && g.map((t, i) => <TxRow key={`${key}-${i}`} t={t} now={now} sub />)}
                </Fragment>
              );
            })}
          </tbody>
        </table>
      )}
    </Panel>
  );
}

function ReceiverPanel({ receiver, metricsEnabled }: { receiver: Receiver; metricsEnabled: boolean }) {
  if (!metricsEnabled) return null;
  const clipping = receiver.clipping_ratio;
  return (
    <Panel id="receiver" title="Receiver">
      <p class={receiver.ok ? "health ok" : "health bad"}>
        <StatusIcon ok={receiver.ok} />
        {receiver.ok ? "rtl_airband is running" : `No metrics from rtl_airband: ${receiver.error}`}
      </p>
      <dl class="facts">
        <div>
          <dt>Clipped samples</dt>
          <dd>{clipping === null ? "–" : `${(clipping * 100).toFixed(3)} %`}</dd>
        </div>
        <div>
          <dt>Input power</dt>
          <dd>{receiver.input_dbfs === null ? "–" : `${receiver.input_dbfs.toFixed(1)} dBFS`}</dd>
        </div>
        <div>
          <dt>Buffer overflows</dt>
          <dd>{receiver.overflows}</dd>
        </div>
        <div>
          <dt>Version</dt>
          <dd>{receiver.version || "–"}</dd>
        </div>
      </dl>
    </Panel>
  );
}

// A box that can be folded away; whether it is folded is remembered per browser.
function Panel({ id, title, children }: { id: string; title: string; children: ComponentChildren }) {
  const key = `hub.collapsed.${id}`;
  const [open] = useState(() => {
    try {
      return localStorage.getItem(key) !== "1";
    } catch {
      return true;
    }
  });
  return (
    <details
      class={`panel ${id}`}
      open={open}
      onToggle={(e) => {
        try {
          localStorage.setItem(key, e.currentTarget.open ? "0" : "1");
        } catch {
          /* not remembered, still folded */
        }
      }}
    >
      <summary>
        <h2>{title}</h2>
      </summary>
      <div class="panel-body">{children}</div>
    </details>
  );
}
