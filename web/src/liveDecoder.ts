// Low-latency live playback: the page reads the MP3 stream, decodes it with WebCodecs and plays
// it through Web Audio, about half a second behind. Given the stream URL directly, browsers
// buffer seconds of a 6 KB/s stream before they start (Chrome ~20 s, Firefox ~5 s) and stay that
// far behind for the whole session. Browsers without WebCodecs fall back to exactly that.

const TARGET_AHEAD = 0.4; // seconds of audio scheduled ahead of the speaker after (re)starting
const MAX_AHEAD = 2.5; // drop audio when further ahead than this (a throttled background tab)

// MPEG audio frame headers (ISO 11172-3 / 13818-3), Layer III only: rtl_airband writes MP3
const BITRATES = {
  v1: [0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320],
  v2: [0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160], // MPEG-2 and 2.5
};
const RATES: Record<number, number[]> = { 3: [44100, 48000, 32000], 2: [22050, 24000, 16000], 0: [11025, 12000, 8000] };

interface Frame {
  length: number;
  sampleRate: number;
  channels: number;
  samples: number;
}

function parseHeader(b: Uint8Array, i: number): Frame | null {
  if (b[i] !== 0xff || (b[i + 1] & 0xe0) !== 0xe0) return null;
  const version = (b[i + 1] >> 3) & 3; // 3 = MPEG-1, 2 = MPEG-2, 0 = MPEG-2.5
  const layer = (b[i + 1] >> 1) & 3; // 1 = Layer III
  const bitrateIx = b[i + 2] >> 4;
  const rateIx = (b[i + 2] >> 2) & 3;
  if (version === 1 || layer !== 1 || bitrateIx === 0 || bitrateIx === 15 || rateIx === 3) return null;
  const padding = (b[i + 2] >> 1) & 1;
  const sampleRate = RATES[version][rateIx];
  const v1 = version === 3;
  const kbps = (v1 ? BITRATES.v1 : BITRATES.v2)[bitrateIx];
  const length = Math.floor(((v1 ? 144 : 72) * kbps * 1000) / sampleRate) + padding;
  return { length, sampleRate, channels: (b[i + 3] >> 6) === 3 ? 1 : 2, samples: v1 ? 1152 : 576 };
}

export async function liveDecoderSupported(): Promise<boolean> {
  if (typeof AudioDecoder === "undefined" || typeof AudioContext === "undefined") return false;
  try {
    return !!(await AudioDecoder.isConfigSupported({ codec: "mp3", sampleRate: 8000, numberOfChannels: 1 })).supported;
  } catch {
    return false;
  }
}

export interface LiveDecoderEvents {
  playing: () => void; // audio is reaching the speakers
  waiting: () => void; // ran out of audio; waiting for more
  end: (message: string) => void; // the stream ended or failed; not called after stop()
}

export class LiveDecoder {
  private readonly ctl = new AbortController();
  private readonly ctx: AudioContext;
  private readonly gain: GainNode;
  private decoder: AudioDecoder | null = null;
  private pending = new Uint8Array(0);
  private timestamp = 0; // microseconds, for the decoder
  private next = 0; // context time the next decoded audio starts at
  private playing = false;
  private stopped = false;
  private timer = 0;

  /** Must be called from a click (or other user gesture), so the AudioContext may start. */
  constructor(
    private readonly mount: string,
    volume: number,
    private readonly on: LiveDecoderEvents,
  ) {
    this.ctx = new AudioContext({ latencyHint: "playback" });
    this.gain = this.ctx.createGain();
    this.gain.gain.value = volume;
    this.gain.connect(this.ctx.destination);
    this.ctx.resume().catch(() => undefined);
    // notice running dry between transmissions of the network, not only when audio arrives
    this.timer = setInterval(() => this.checkUnderrun(), 250);
    this.read();
  }

  setVolume(v: number) {
    this.gain.gain.value = v;
  }

  /** Seconds of audio scheduled ahead of the speaker. */
  behind(): number | null {
    return this.playing ? Math.max(0, this.next - this.ctx.currentTime) + (this.ctx.outputLatency || 0) : null;
  }

  stop() {
    if (this.stopped) return;
    this.stopped = true;
    this.ctl.abort();
    clearInterval(this.timer);
    try {
      this.decoder?.close();
    } catch {
      /* already closed */
    }
    this.ctx.close().catch(() => undefined);
  }

  private end(message: string) {
    if (this.stopped) return;
    this.stop();
    this.on.end(message);
  }

  private async read() {
    try {
      const r = await fetch(this.mount, { signal: this.ctl.signal, cache: "no-store" });
      if (!r.ok || !r.body) {
        this.end(r.status === 404 ? "No stream from rtl_airband right now" : `The hub answered ${r.status}`);
        return;
      }
      const reader = r.body.getReader();
      for (;;) {
        const { value, done } = await reader.read();
        if (done) break;
        this.feed(value);
      }
      this.end("The stream ended; rtl_airband may have stopped");
    } catch (e) {
      if (!this.ctl.signal.aborted) this.end(e instanceof Error && e.name === "EncodingError" ? "Cannot decode the stream" : "Lost the connection to the hub");
    }
  }

  // splits the byte stream into whole frames and hands them to the decoder
  private feed(chunk: Uint8Array) {
    const b = new Uint8Array(this.pending.length + chunk.length);
    b.set(this.pending);
    b.set(chunk, this.pending.length);
    let i = 0;
    while (i + 4 <= b.length) {
      const f = parseHeader(b, i);
      if (!f) {
        i++; // not in sync (the first bytes, or a damaged frame): look for the next header
        continue;
      }
      if (i + f.length > b.length) break; // the rest of this frame has not arrived yet
      this.decode(b.subarray(i, i + f.length), f);
      i += f.length;
    }
    this.pending = b.slice(i);
  }

  private decode(data: Uint8Array, f: Frame) {
    if (this.stopped) return;
    if (!this.decoder) {
      this.decoder = new AudioDecoder({
        output: (a) => this.schedule(a),
        error: () => this.end("Cannot decode the stream"),
      });
      this.decoder.configure({ codec: "mp3", sampleRate: f.sampleRate, numberOfChannels: f.channels });
    }
    this.decoder.decode(new EncodedAudioChunk({ type: "key", timestamp: this.timestamp, data }));
    this.timestamp += (f.samples / f.sampleRate) * 1e6;
  }

  private schedule(a: AudioData) {
    if (this.stopped) {
      a.close();
      return;
    }
    const now = this.ctx.currentTime;
    const buf = this.ctx.createBuffer(a.numberOfChannels, a.numberOfFrames, a.sampleRate);
    for (let ch = 0; ch < a.numberOfChannels; ch++) {
      a.copyTo(buf.getChannelData(ch), { planeIndex: ch, format: "f32-planar" });
    }
    a.close();

    if (this.next < now) {
      // first audio, or we ran dry: start again a little ahead so small network jitter is absorbed
      this.next = now + TARGET_AHEAD;
    } else if (this.next - now > MAX_AHEAD) {
      return; // too far behind live; drop this piece to catch up
    }
    const src = this.ctx.createBufferSource();
    src.buffer = buf;
    src.connect(this.gain);
    src.start(this.next);
    this.next += buf.duration;
    if (!this.playing) {
      this.playing = true;
      this.on.playing();
    }
  }

  private checkUnderrun() {
    if (this.playing && this.next < this.ctx.currentTime) {
      this.playing = false;
      this.on.waiting();
    }
  }
}
