import { useEffect, useRef, useState } from "preact/hooks";
import type { Segment } from "./api";

export interface Decoded {
  duration: number;
  pps: number; // peaks per second
  peaks: Uint8Array; // 0 = below -60 dBFS, 1..255 = -60..0 dBFS
  transmissions: Segment[];
}

interface Props {
  wave: Decoded;
  from: number; // visible window, seconds into the recording
  to: number;
  playhead: number | null;
  marks?: number[]; // bookmarks, seconds into the recording
  label: (t: number) => string; // hover label for a time
  onPick: (t: number, snapped: Segment | null) => void;
  height: number;
  ariaLabel: string;
}

// Peaks are stored as -60..0 dBFS (1..255). Drawn on that log scale, speech fills the whole
// height; drawn linearly, distant transmissions vanish. The square root of the linear amplitude
// keeps the shape of speech and still shows quiet signals.
const heights = new Float32Array(256);
for (let v = 1; v < 256; v++) heights[v] = Math.sqrt(10 ** ((((v - 1) / 254) * 60 - 60) / 20));

function cssVar(el: Element, name: string) {
  return getComputedStyle(el).getPropertyValue(name).trim();
}

// A waveform like an audio editor's: the loudest sample per pixel, mirrored around the middle,
// with detected transmissions shaded behind it.
export function Wave({ wave, from, to, playhead, marks, label, onPick, height, ariaLabel }: Props) {
  const canvas = useRef<HTMLCanvasElement>(null);
  const [width, setWidth] = useState(0);
  const [hover, setHover] = useState<number | null>(null);

  useEffect(() => {
    const el = canvas.current;
    if (!el) return;
    const ro = new ResizeObserver(([e]) => setWidth(e.contentRect.width));
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  useEffect(() => {
    const el = canvas.current;
    if (!el || width === 0) return;
    const dpr = window.devicePixelRatio || 1;
    const W = Math.round(width * dpr);
    const H = Math.round(height * dpr);
    if (el.width !== W) el.width = W;
    if (el.height !== H) el.height = H;
    const ctx = el.getContext("2d")!;
    ctx.clearRect(0, 0, W, H);

    const span = Math.max(to - from, 0.001);
    const x = (t: number) => ((t - from) / span) * W;
    const mid = H / 2;

    ctx.fillStyle = cssVar(el, "--tx-band");
    for (const s of wave.transmissions) {
      if (s.end < from || s.start > to) continue;
      const x0 = x(s.start);
      ctx.fillRect(x0, 0, Math.max(x(s.end) - x0, 2 * dpr), H);
    }

    ctx.fillStyle = cssVar(el, "--wave-track");
    ctx.fillRect(0, mid - dpr / 2, W, dpr);

    ctx.fillStyle = cssVar(el, "--wave");
    const n = wave.peaks.length;
    for (let px = 0; px < W; px++) {
      const t0 = from + (px / W) * span;
      const t1 = from + ((px + 1) / W) * span;
      let i0 = Math.floor(t0 * wave.pps);
      const i1 = Math.min(n, Math.max(i0 + 1, Math.ceil(t1 * wave.pps)));
      let peak = 0;
      for (i0 = Math.max(0, i0); i0 < i1; i0++) peak = Math.max(peak, wave.peaks[i0]);
      if (peak === 0) continue;
      const h = Math.max(dpr, heights[peak] * (H / 2 - dpr));
      ctx.fillRect(px, mid - h, 1, h * 2);
    }

    if (marks?.length) {
      // a line with a small flag at the top, so it reads differently from the playhead
      ctx.fillStyle = cssVar(el, "--mark");
      const f = Math.min(6 * dpr, H / 4);
      for (const m of marks) {
        if (m < from || m > to) continue;
        const mx = Math.round(x(m));
        ctx.fillRect(mx, 0, dpr, H);
        ctx.beginPath();
        ctx.moveTo(mx, 0);
        ctx.lineTo(mx + f, f / 2);
        ctx.lineTo(mx, f);
        ctx.fill();
      }
    }

    if (playhead !== null && playhead >= from && playhead <= to) {
      ctx.fillStyle = cssVar(el, "--playhead");
      ctx.fillRect(x(playhead) - dpr, 0, 2 * dpr, H);
    }
    if (hover !== null) {
      ctx.fillStyle = cssVar(el, "--wave-hover");
      ctx.fillRect(x(hover), 0, dpr, H);
    }
  }, [wave, from, to, playhead, marks, hover, width, height]);

  const timeAt = (clientX: number) => {
    const r = canvas.current!.getBoundingClientRect();
    return from + ((clientX - r.left) / r.width) * (to - from);
  };

  // a click near a transmission (within 4 px) starts at that transmission
  const snap = (t: number): Segment | null => {
    const tolerance = (4 / Math.max(width, 1)) * (to - from);
    return wave.transmissions.find((s) => t >= s.start - tolerance && t <= s.end + tolerance) ?? null;
  };

  const hoverPct = hover === null ? 0 : ((hover - from) / (to - from)) * 100;
  return (
    <div class="wave" style={{ height: `${height}px` }}>
      <canvas
        ref={canvas}
        role="img"
        aria-label={ariaLabel}
        onMouseMove={(e) => setHover(timeAt(e.clientX))}
        onMouseLeave={() => setHover(null)}
        onClick={(e) => {
          const t = Math.min(wave.duration, Math.max(0, timeAt(e.clientX)));
          onPick(t, snap(t));
        }}
      />
      {hover !== null && (
        <span class={hoverPct > 80 ? "wave-label left" : "wave-label"} style={{ left: `${hoverPct}%` }}>
          {label(hover)}
        </span>
      )}
    </div>
  );
}

export function decode(w: { duration: number; peaks_per_second: number; peaks: string; transmissions: Segment[] }): Decoded {
  const bin = atob(w.peaks);
  const peaks = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) peaks[i] = bin.charCodeAt(i);
  return { duration: w.duration, pps: w.peaks_per_second, peaks, transmissions: w.transmissions };
}
