// One player for the whole app: the live stream and the recordings share the audio element
// and the bar at the bottom, and playback carries on between pages.
import { createContext, type ComponentChildren } from "preact";
import { useCallback, useContext, useEffect, useMemo, useRef, useState } from "preact/hooks";
import { apiSend, type Bookmark, type Mount, type Recording } from "./api";
import { clockFmt, dayFmt } from "./format";
import { LiveDecoder, liveDecoderSupported } from "./liveDecoder";
import { Link, navigate } from "./router";
import type { Decoded } from "./Wave";

export const LEAD_IN = 0.3; // start a transmission slightly early so its first word is not clipped
export const BOOKMARKS_CHANGED = "hub-bookmarks-changed";

// known before the first click: the decoder's AudioContext must start inside that click
let decoderOk = false;
liveDecoderSupported().then((ok) => (decoderOk = ok));

export const at = (rec: Recording, t: number) => new Date(new Date(rec.start).getTime() + t * 1000);

// loading: connecting or buffering; nothing is heard yet
export type Status = "idle" | "loading" | "playing" | "paused" | "error";

export interface PlayerState {
  live: { mount: string } | null; // set while the live stream is the source
  rec: Recording | null; // set while a recording is the source
  wave: Decoded | null;
  status: Status;
  error: string | null;
  position: number; // seconds into the recording
}

const IDLE: PlayerState = { live: null, rec: null, wave: null, status: "idle", error: null, position: 0 };

function readVolume(): number {
  try {
    const v = Number(localStorage.getItem("hub.volume"));
    return localStorage.getItem("hub.volume") !== null && v >= 0 && v <= 1 ? v : 1;
  } catch {
    return 1;
  }
}

function usePlayerState() {
  const audio = useRef<HTMLAudioElement>(new Audio());
  const offset = useRef(0); // the recording time the current audio URL starts at
  const decoder = useRef<LiveDecoder | null>(null); // the live stream, when decoded in the page
  const [state, setState] = useState<PlayerState>(IDLE);
  const [skipSilence, setSkipSilence] = useState(false);
  const [volume, setVolumeState] = useState(readVolume);
  const [behind, setBehind] = useState<number | null>(null); // live: seconds of audio buffered ahead
  const cur = useRef({ state, skipSilence, volume });
  cur.current = { state, skipSilence, volume };

  const dropDecoder = () => {
    decoder.current?.stop();
    decoder.current = null;
  };

  const start = (src: string, next: PlayerState) => {
    const el = audio.current;
    el.src = src;
    setState(next);
    el.play().catch((e) => {
      // a newer src replacing this one aborts the play; that is not an error
      if (e?.name !== "AbortError") setState((s) => ({ ...s, status: "error", error: "The browser would not start playback" }));
    });
  };

  const play = useCallback((rec: Recording, wave: Decoded, t: number) => {
    dropDecoder();
    offset.current = Math.max(0, t);
    start(`/api/recordings/audio?path=${encodeURIComponent(rec.path)}&from=${offset.current.toFixed(3)}`, {
      ...IDLE,
      rec,
      wave,
      status: "loading",
      position: offset.current,
    });
  }, []);

  const playLive = useCallback((mount: string) => {
    dropDecoder();
    setBehind(null);
    const next: PlayerState = { ...IDLE, live: { mount }, status: "loading" };
    if (decoderOk) {
      // played through Web Audio; the audio element is not used
      const el = audio.current;
      el.removeAttribute("src");
      el.load();
      const set = (status: Status, error: string | null = null) => setState((s) => (s.live ? { ...s, status, error } : s));
      decoder.current = new LiveDecoder(mount, cur.current.volume, {
        playing: () => set("playing"),
        waiting: () => set("loading"),
        end: (error) => {
          decoder.current = null;
          set("error", error);
        },
      });
      setState(next);
    } else {
      start(mount, next); // the browser's own buffering: starts late and stays behind
    }
  }, []);

  // live: stop drops the connection, so the next play starts at the live edge again
  const stopLive = useCallback(() => {
    const el = audio.current;
    if (decoder.current) dropDecoder();
    else {
      el.removeAttribute("src");
      el.load();
    }
    setBehind(null);
    setState((s) => ({ ...s, status: "paused" }));
  }, []);

  const toggle = useCallback(() => {
    const { state: s } = cur.current;
    const el = audio.current;
    if (s.live) {
      if (s.status === "playing" || s.status === "loading") stopLive();
      else playLive(s.live.mount);
      return;
    }
    if (!s.rec) return;
    if (el.paused) el.play().catch(() => undefined);
    else el.pause();
  }, [playLive, stopLive]);

  const jump = useCallback(
    (dir: 1 | -1) => {
      const { rec, wave, position } = cur.current.state;
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

  const setVolume = useCallback((v: number) => {
    audio.current.volume = v;
    decoder.current?.setVolume(v);
    setVolumeState(v);
    try {
      localStorage.setItem("hub.volume", String(v));
    } catch {
      /* not remembered, still applied */
    }
  }, []);

  useEffect(() => {
    const el = audio.current;
    el.volume = volume;
    const status = (status: Status, error: string | null = null) => () => {
      if (!el.getAttribute("src")) return; // a stopped live stream
      setState((s) => (s.status === status && s.error === error ? s : { ...s, status, error }));
    };
    const ended = () =>
      setState((s) => (s.live ? { ...s, status: "error", error: "The stream ended; rtl_airband may have stopped" } : { ...s, status: "paused" }));
    const handlers: [string, () => void][] = [
      ["waiting", status("loading")],
      ["playing", status("playing")],
      ["pause", status("paused")],
      ["ended", ended],
      ["error", status("error", "Cannot play this audio")],
    ];
    for (const [ev, fn] of handlers) el.addEventListener(ev, fn);
    return () => {
      for (const [ev, fn] of handlers) el.removeEventListener(ev, fn);
      dropDecoder();
      el.pause();
    };
  }, []);

  // recordings: the playhead and skip-silence need a per-frame check, but only while audio plays
  useEffect(() => {
    if (state.status !== "playing" || !state.rec) return;
    const el = audio.current;
    let frame = 0;
    const tick = () => {
      const { state: s, skipSilence: skip } = cur.current;
      if (s.rec && s.wave && !el.paused) {
        const pos = offset.current + el.currentTime;
        if (skip) {
          const inside = s.wave.transmissions.some((seg) => pos >= seg.start - LEAD_IN - 0.2 && pos <= seg.end + 0.5);
          if (!inside) {
            const next = s.wave.transmissions.find((seg) => seg.start - LEAD_IN > pos);
            if (next) play(s.rec, s.wave, next.start - LEAD_IN);
            else el.pause();
            frame = requestAnimationFrame(tick);
            return;
          }
        }
        setState((st) => (Math.abs(st.position - pos) > 0.04 ? { ...st, position: pos } : st));
      }
      frame = requestAnimationFrame(tick);
    };
    frame = requestAnimationFrame(tick);
    return () => cancelAnimationFrame(frame);
  }, [state.status, state.rec, play]);

  // live: how far the player is behind what has arrived
  useEffect(() => {
    if (!state.live || state.status !== "playing") return;
    const el = audio.current;
    const id = setInterval(() => {
      if (decoder.current) return setBehind(decoder.current.behind());
      const b = el.buffered;
      if (b.length) setBehind(Math.max(0, b.end(b.length - 1) - el.currentTime));
    }, 1000);
    return () => clearInterval(id);
  }, [state.live, state.status]);

  return { state, play, playLive, stopLive, toggle, jump, skipSilence, setSkipSilence, volume, setVolume, behind };
}

export type Player = ReturnType<typeof usePlayerState>;

const PlayerContext = createContext<Player | null>(null);

export function PlayerProvider({ children }: { children: ComponentChildren }) {
  const player = usePlayerState();
  return <PlayerContext.Provider value={player}>{children}</PlayerContext.Provider>;
}

export function usePlayer(): Player {
  const p = useContext(PlayerContext);
  if (!p) throw new Error("usePlayer outside PlayerProvider");
  return p;
}

const busy = (s: PlayerState) => s.status === "playing" || s.status === "loading";

function PlayIcon({ playing }: { playing: boolean }) {
  return (
    <svg viewBox="0 0 16 16" aria-hidden="true">
      {playing ? <path d="M4.5 3h2.5v10H4.5zM9 3h2.5v10H9z" /> : <path d="M5 3.5v9l7.5-4.5z" />}
    </svg>
  );
}

function Volume({ player }: { player: Player }) {
  const { volume, setVolume } = player;
  const before = useRef(1);
  const muted = volume === 0;
  return (
    <div class="volume">
      <button
        type="button"
        class="volume-icon"
        onClick={() => {
          if (muted) setVolume(before.current || 1);
          else {
            before.current = volume;
            setVolume(0);
          }
        }}
        title={muted ? "Unmute" : "Mute"}
      >
        <svg viewBox="0 0 16 16" aria-hidden="true">
          <path d="M2 6h2.5L8 3v10L4.5 10H2z" />
          {muted ? (
            <path d="M10.5 6l4 4M14.5 6l-4 4" fill="none" stroke-width="1.5" />
          ) : (
            <>
              <path d="M10.5 6a2.5 2.5 0 010 4" fill="none" stroke-width="1.4" opacity={volume > 0 ? 1 : 0.3} />
              <path d="M12 4a5 5 0 010 8" fill="none" stroke-width="1.4" opacity={volume > 0.5 ? 1 : 0.3} />
            </>
          )}
        </svg>
        <span class="visually-hidden">{muted ? "Unmute" : "Mute"}</span>
      </button>
      <input
        type="range"
        min={0}
        max={1}
        step={0.05}
        value={volume}
        aria-label="Volume"
        style={{ "--fill": `${volume * 100}%` }}
        onInput={(e) => setVolume(Number(e.currentTarget.value))}
      />
    </div>
  );
}

export function PlayerBar({ hint, bookmarksEnabled, mount }: { hint: string | null; bookmarksEnabled: boolean; mount?: Mount }) {
  const player = usePlayer();
  const { state, toggle, jump, skipSilence, setSkipSilence, behind } = player;
  const [adding, setAdding] = useState(false);
  const [saved, setSaved] = useState<Bookmark | null>(null);

  useEffect(() => {
    const key = (e: KeyboardEvent) => {
      if (e.target instanceof HTMLInputElement || e.target instanceof HTMLTextAreaElement || e.target instanceof HTMLButtonElement || e.target instanceof HTMLAnchorElement) return;
      if (e.key === " " && (cur.current.live || cur.current.rec)) {
        e.preventDefault();
        toggle();
      } else if (e.key === "ArrowRight" && e.altKey) jump(1);
      else if (e.key === "ArrowLeft" && e.altKey) jump(-1);
    };
    addEventListener("keydown", key);
    return () => removeEventListener("keydown", key);
  }, [toggle, jump]);
  const cur = useRef(state);
  cur.current = state;

  // a new source closes the bookmark form
  const sourceKey = state.live ? "live" : (state.rec?.path ?? "");
  useEffect(() => setAdding(false), [sourceKey]);

  if (!state.live && !state.rec) {
    return hint ? (
      <div class="player idle" role="region" aria-label="Player">
        {hint}
      </div>
    ) : null;
  }

  const loading = state.status === "loading";
  const playButton = (
    <button
      type="button"
      class={loading ? "play loading" : "play"}
      onClick={toggle}
      title={state.live ? (busy(state) ? "Stop (Space)" : "Listen live (Space)") : "Play or pause (Space)"}
      aria-busy={loading}
    >
      <PlayIcon playing={busy(state)} />
      <span class="visually-hidden">{busy(state) ? (state.live ? "Stop" : "Pause") : "Play"}</span>
    </button>
  );

  if (state.live) {
    let detail: string;
    if (state.status === "error") detail = state.error ?? "Playback failed";
    else if (loading) detail = behind === null ? "Connecting…" : "Buffering…";
    else if (state.status === "playing") detail = behind === null ? "Playing" : `${behind < 10 ? behind.toFixed(1) : Math.round(behind)} s behind the receiver`;
    else detail = "Stopped";
    return (
      <div class="player" role="region" aria-label="Player">
        <div class="player-buttons">{playButton}</div>
        <div class="player-where">
          <strong>
            <span class={state.status === "playing" ? "live-dot on" : "live-dot"} aria-hidden="true" />
            Live{mount?.info.name ? `: ${mount.info.name}` : ""}
          </strong>
          <span class={state.status === "error" ? "secondary error" : "secondary"} role="status">
            {detail}
          </span>
        </div>
        <Volume player={player} />
      </div>
    );
  }

  const rec = state.rec!;
  const when = at(rec, state.position);
  return (
    <div class="player" role="region" aria-label="Player">
      <div class="player-buttons">
        <button type="button" onClick={() => jump(-1)} title="Previous transmission (Alt+Left)">
          <svg viewBox="0 0 16 16" aria-hidden="true">
            <path d="M4 3v10M13 3.5v9L6 8z" />
          </svg>
          <span class="visually-hidden">Previous transmission</span>
        </button>
        {playButton}
        <button type="button" onClick={() => jump(1)} title="Next transmission (Alt+Right)">
          <svg viewBox="0 0 16 16" aria-hidden="true">
            <path d="M12 3v10M3 3.5v9L10 8z" />
          </svg>
          <span class="visually-hidden">Next transmission</span>
        </button>
      </div>
      <div class="player-where">
        <strong>{clockFmt.format(when)}</strong>
        <span class={state.status === "error" ? "secondary error" : "secondary"}>
          {state.status === "error" ? state.error : loading ? "Loading…" : dayFmt.format(when)}
        </span>
      </div>
      <label class="skip">
        <input type="checkbox" checked={skipSilence} onChange={(e) => setSkipSilence(e.currentTarget.checked)} />
        Skip silence
      </label>
      <Volume player={player} />
      <div class="player-links">
        {bookmarksEnabled &&
          (saved ? (
            <span class="saved" role="status">
              Saved “{saved.name}”.{" "}
              <Link href={`/recordings?bookmark=${saved.id}`} class="player-link">
                Open it
              </Link>
            </span>
          ) : (
            <button type="button" class="player-link" aria-expanded={adding} onClick={() => setAdding(!adding)}>
              Add bookmark
            </button>
          ))}
        <button
          type="button"
          class="player-link"
          onClick={() => {
            const d = new Date(rec.start);
            const date = `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
            navigate(`/recordings?date=${date}`);
          }}
        >
          Show its day
        </button>
      </div>
      {adding && (
        <AddBookmark
          rec={rec}
          offset={bookmarkOffset(state.wave, state.position)}
          onCancel={() => setAdding(false)}
          onSaved={(b) => {
            setAdding(false);
            setSaved(b);
            dispatchEvent(new Event(BOOKMARKS_CHANGED));
            setTimeout(() => setSaved((c) => (c === b ? null : c)), 8000);
          }}
        />
      )}
    </div>
  );
}

// A bookmark made while a transmission plays points at its start, so playing it gives the
// whole transmission; one made in silence points where the player is.
function bookmarkOffset(wave: Decoded | null, position: number): number {
  const tx = wave?.transmissions.find((s) => position >= s.start - LEAD_IN && position <= s.end + 1);
  return tx ? Math.max(0, tx.start - LEAD_IN) : position;
}

function AddBookmark({ rec, offset, onCancel, onSaved }: { rec: Recording; offset: number; onCancel: () => void; onSaved: (b: Bookmark) => void }) {
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // where it was when the form opened, not where the playhead has moved on to since
  const [where] = useState(offset);
  const input = useRef<HTMLInputElement>(null);
  useEffect(() => input.current?.focus(), []);
  const label = useMemo(() => clockFmt.format(at(rec, where)), [rec, where]);

  return (
    <form
      class="add-bookmark"
      onSubmit={(e) => {
        e.preventDefault();
        if (!name.trim() || busy) return;
        setBusy(true);
        apiSend<Bookmark>("POST", "/api/bookmarks", { name: name.trim(), path: rec.path, offset: where })
          .then((b) => b && onSaved(b))
          .catch((e) => {
            setError(String(e.message ?? e));
            setBusy(false);
          });
      }}
      onKeyDown={(e) => e.key === "Escape" && onCancel()}
    >
      <label>
        <span>
          Bookmark at <strong>{label}</strong>
        </span>
        <input ref={input} value={name} maxLength={100} placeholder="Name it, e.g. Go-around runway 01" onInput={(e) => setName(e.currentTarget.value)} />
      </label>
      <button type="submit" class="primary" disabled={!name.trim() || busy}>
        Save bookmark
      </button>
      <button type="button" onClick={onCancel}>
        Cancel
      </button>
      {error && <p class="form-error">Not saved: {error}</p>}
    </form>
  );
}
