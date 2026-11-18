import { useRef, useState } from "preact/hooks";
import type { Frequency, Live, Mount, Receiver, Transmission } from "./api";
import { ago, clock, isSet, seconds, snr, stationName } from "./format";
import { useLive, type Connection } from "./useLive";

export function App() {
  const { live, connection } = useLive();
  const now = live ? new Date(live.time) : new Date();
  const mount = live?.mounts[0];

  return (
    <div class="page">
      <Header mount={mount} connection={connection} now={now} />
      {live === null ? (
        <p class="empty">{connection === "lost" ? "Cannot reach the hub. Retrying…" : "Connecting to the hub…"}</p>
      ) : (
        <main>
          <Board live={live} />
          <div class="lower">
            <Recent recent={live.recent} metricsEnabled={live.metrics_enabled} now={now} />
            <ReceiverPanel receiver={live.receiver} metricsEnabled={live.metrics_enabled} />
          </div>
        </main>
      )}
    </div>
  );
}

function Header({ mount, connection, now }: { mount?: Mount; connection: Connection; now: Date }) {
  const name = mount?.info.name || "Airband hub";
  return (
    <header class="header">
      <div class="station">
        <h1>{name}</h1>
        {mount && isSet(mount.title_updated) ? (
          <p class="last-heard">
            Last active <strong>{mount.title}</strong>, {ago(mount.title_updated, now)}
          </p>
        ) : (
          <p class="last-heard">No transmission since the receiver started</p>
        )}
      </div>
      <Listen mount={mount} connection={connection} />
    </header>
  );
}

function Listen({ mount, connection }: { mount?: Mount; connection: Connection }) {
  const audio = useRef<HTMLAudioElement>(null);
  const [playing, setPlaying] = useState(false);
  const live = !!mount?.source_connected && connection === "open";

  const toggle = () => {
    const el = audio.current;
    if (!el || !mount) return;
    if (playing) {
      // drop the connection instead of pausing, so the next play is live rather than minutes behind
      el.removeAttribute("src");
      el.load();
      setPlaying(false);
    } else {
      el.src = mount.mount;
      el.play().then(
        () => setPlaying(true),
        () => setPlaying(false),
      );
    }
  };

  let status: string;
  if (connection === "lost") status = "Hub unreachable";
  else if (!mount) status = "No stream from rtl_airband";
  else if (!mount.source_connected) status = "Stream interrupted, waiting for rtl_airband";
  else status = mount.listeners === 1 ? "1 listener" : `${mount.listeners} listeners`;

  return (
    <div class="listen">
      <button type="button" class={playing ? "listen-button playing" : "listen-button"} onClick={toggle} disabled={!mount} aria-pressed={playing}>
        <svg viewBox="0 0 16 16" aria-hidden="true">
          {playing ? <rect x="4" y="4" width="8" height="8" rx="1" /> : <path d="M5 3.5v9l7.5-4.5z" />}
        </svg>
        {playing ? "Stop" : "Listen live"}
      </button>
      <span class={live ? "stream-status ok" : "stream-status bad"}>
        <StatusIcon ok={live} />
        {status}
      </span>
      <audio ref={audio} onEnded={() => setPlaying(false)} onError={() => setPlaying(false)} preload="none" />
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

function Recent({ recent, metricsEnabled, now }: { recent: Transmission[]; metricsEnabled: boolean; now: Date }) {
  return (
    <section class="recent" aria-labelledby="recent-title">
      <h2 id="recent-title">Recent transmissions</h2>
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
            {recent.map((t, i) => (
              <tr key={`${t.end}-${t.freq}-${i}`}>
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
            ))}
          </tbody>
        </table>
      )}
    </section>
  );
}

function ReceiverPanel({ receiver, metricsEnabled }: { receiver: Receiver; metricsEnabled: boolean }) {
  if (!metricsEnabled) return null;
  const clipping = receiver.clipping_ratio;
  return (
    <section class="receiver" aria-labelledby="receiver-title">
      <h2 id="receiver-title">Receiver</h2>
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
    </section>
  );
}
