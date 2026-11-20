import { useEffect, useMemo, useRef, useState } from "preact/hooks";
import { apiSend, type Bookmark, type Day, type Recording, type Segment, type Waveform } from "./api";
import { clockFmt, dayFmt, seconds } from "./format";
import { at, BOOKMARKS_CHANGED, LEAD_IN, usePlayer, type Player } from "./player";
import { Link, navigate } from "./router";
import { decode, Wave, type Decoded } from "./Wave";

const tz = Intl.DateTimeFormat().resolvedOptions().timeZone;
const DETAIL_SPAN = 300; // seconds shown in the detail view

const hourFmt = new Intl.DateTimeFormat([], { hour: "2-digit", minute: "2-digit", hourCycle: "h23" });
const shortDayFmt = new Intl.DateTimeFormat([], { weekday: "short", day: "numeric" });
const monthFmt = new Intl.DateTimeFormat([], { month: "long", year: "numeric" });

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

export function loadWave(rec: Recording): Promise<Decoded> {
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


type Order = "oldest" | "newest";

// remembered per browser; storage can be unavailable (private windows), so it is optional
function useOrder(): [Order, (o: Order) => void] {
  const [order, setOrder] = useState<Order>(() => {
    try {
      return localStorage.getItem("hub.hourOrder") === "newest" ? "newest" : "oldest";
    } catch {
      return "oldest";
    }
  });
  const set = (o: Order) => {
    setOrder(o);
    try {
      localStorage.setItem("hub.hourOrder", o);
    } catch {
      /* not remembered, still applied */
    }
  };
  return [order, set];
}

function OrderToggle({ order, onChange }: { order: Order; onChange: (o: Order) => void }) {
  return (
    <div class="segmented" role="group" aria-label="Order of the hours">
      <button type="button" aria-pressed={order === "oldest"} onClick={() => onChange("oldest")}>
        Chronological
      </button>
      <button type="button" aria-pressed={order === "newest"} onClick={() => onChange("newest")}>
        Newest first
      </button>
    </div>
  );
}

export function Recordings({ enabled, bookmarksEnabled, url }: { enabled: boolean; bookmarksEnabled: boolean; url: URL }) {
  const bookmarkId = Number(url.searchParams.get("bookmark")) || null;
  const view = bookmarksEnabled && (bookmarkId !== null || url.searchParams.get("view") === "bookmarks") ? "bookmarks" : "all";
  const [bookmarksVersion, setBookmarksVersion] = useState(0);
  const bookmarks = useJSON<Bookmark[]>(enabled && bookmarksEnabled ? "/api/bookmarks" : null, bookmarksVersion);
  const bookmarksChanged = () => setBookmarksVersion((n) => n + 1);
  const days = useJSON<Day[]>(enabled && view === "all" ? `/api/recordings/days?tz=${encodeURIComponent(tz)}` : null);
  const date = url.searchParams.get("date") ?? days.data?.[0]?.date ?? null;
  const [refresh, setRefresh] = useState(0);
  const recs = useJSON<Recording[]>(date && view === "all" ? `/api/recordings?date=${date}&tz=${encodeURIComponent(tz)}` : null, refresh);
  const player = usePlayer();
  useEffect(() => {
    addEventListener(BOOKMARKS_CHANGED, bookmarksChanged);
    return () => removeEventListener(BOOKMARKS_CHANGED, bookmarksChanged);
  }, []);
  const [open, setOpen] = useState<{ path: string; focus: number } | null>(null);
  const [order, setOrder] = useOrder();
  const hours = recs.data ? (order === "newest" ? [...recs.data].reverse() : recs.data) : null;

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

  const marksFor = (rec: Recording) => (bookmarks.data ?? []).filter((b) => b.path === rec.path.replace(/\.tmp$/, ""));
  const switcher = bookmarksEnabled && <ViewToggle view={view} count={bookmarks.data?.length ?? null} />;

  if (view === "bookmarks") {
    return (
      <main class="recordings">
        {switcher}
        <Bookmarks
          list={bookmarks.data}
          error={bookmarks.error}
          selectedId={bookmarkId}
          player={player}
          onChanged={bookmarksChanged}
        />
      </main>
    );
  }

  return (
    <main class="recordings">
      {switcher}
      <DayList days={days.data} error={days.error} selected={date} />
      <section class="day" aria-labelledby="day-title">
        {date && (
          <div class="day-head">
            <h2 id="day-title">{dayFmt.format(localDate(date))}</h2>
            <OrderToggle order={order} onChange={setOrder} />
          </div>
        )}
        {recs.error && <p class="empty">Cannot list the recordings: {recs.error}</p>}
        {days.data?.length === 0 && <p class="empty">No recordings yet. They appear here once rtl_airband's file output has written one.</p>}
        {recs.data?.length === 0 && <p class="empty">No recordings start on this day.</p>}
        <ol class="hours">
          {hours?.map((rec) => (
            <HourRow
              key={rec.path}
              rec={rec}
              player={player}
              open={open?.path === rec.path ? open.focus : null}
              onOpen={(focus) => setOpen({ path: rec.path, focus })}
              marks={marksFor(rec)}
            />
          ))}
        </ol>
      </section>
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

function HourRow({
  rec,
  player,
  open,
  onOpen,
  marks: bookmarks,
}: {
  rec: Recording;
  player: Player;
  open: number | null;
  onOpen: (focus: number) => void;
  marks?: Bookmark[];
}) {
  const markKey = bookmarks?.map((b) => b.offset).join(",") ?? "";
  const marks = useMemo(() => (markKey ? markKey.split(",").map(Number) : undefined), [markKey]);
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
          marks={marks}
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
            marks={marks}
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

function ViewToggle({ view, count }: { view: "all" | "bookmarks"; count: number | null }) {
  return (
    <div class="segmented view-toggle" role="group" aria-label="Show">
      <button type="button" aria-pressed={view === "all"} onClick={() => view !== "all" && navigate("/recordings")}>
        All recordings
      </button>
      <button type="button" aria-pressed={view === "bookmarks"} onClick={() => view !== "bookmarks" && navigate("/recordings?view=bookmarks")}>
        Bookmarks{count ? ` (${count})` : ""}
      </button>
    </div>
  );
}

const bookmarkHref = (b: Bookmark) => `/recordings?bookmark=${b.id}`;

function playBookmark(player: Player, b: Bookmark) {
  const rec = b.recording;
  if (rec) loadWave(rec).then((w) => player.play(rec, w, b.offset));
}

function Bookmarks({
  list,
  error,
  selectedId,
  player,
  onChanged,
}: {
  list: Bookmark[] | null;
  error: string | null;
  selectedId: number | null;
  player: Player;
  onChanged: () => void;
}) {
  const selected = list && (selectedId === null ? list[0] : list.find((b) => b.id === selectedId));

  // the address always names the bookmark on screen, so it can be shared as it is
  useEffect(() => {
    if (selectedId === null && list?.[0]) navigate(bookmarkHref(list[0]), true);
  }, [selectedId, list]);

  return (
    <>
      <nav class="bookmark-list" aria-label="Bookmarks">
        {error && <p class="empty">Cannot list bookmarks: {error}</p>}
        {list === null && !error && <p class="empty">Loading bookmarks…</p>}
        <ul>
          {list?.map((b) => (
            <li key={b.id}>
              <a
                href={bookmarkHref(b)}
                class="bookmark-link"
                aria-current={b.id === selected?.id ? "page" : undefined}
                onClick={(e) => {
                  if (e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
                  e.preventDefault();
                  navigate(bookmarkHref(b));
                  playBookmark(player, b);
                }}
              >
                <span class="bookmark-name">{b.name}</span>
                <span class="bookmark-when">
                  {shortDayFmt.format(new Date(b.at))} {hourFmt.format(new Date(b.at))}
                  {b.freq && ` · ${b.freq}`}
                </span>
              </a>
            </li>
          ))}
        </ul>
      </nav>
      <section class="day">
        {list?.length === 0 && (
          <p class="empty">
            While a recording plays, use <strong>Add bookmark</strong> in the player to save that moment with a name. Bookmarks are listed here and each has
            its own link to share.
          </p>
        )}
        {list && list.length > 0 && !selected && (
          <p class="empty">
            There is no such bookmark; it may have been deleted. <Link href="/recordings?view=bookmarks">Show all bookmarks</Link>
          </p>
        )}
        {selected && (
          <BookmarkDetail
            key={selected.id}
            b={selected}
            sameHour={list!.filter((o) => o.path === selected.path)}
            player={player}
            onChanged={onChanged}
          />
        )}
      </section>
    </>
  );
}

function BookmarkDetail({ b, sameHour, player, onChanged }: { b: Bookmark; sameHour: Bookmark[]; player: Player; onChanged: () => void }) {
  const [focus, setFocus] = useState(b.offset);
  const [renaming, setRenaming] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [shared, setShared] = useState<"copied" | "show" | null>(null);
  const [error, setError] = useState<string | null>(null);
  const when = new Date(b.at);
  const link = `${location.origin}${bookmarkHref(b)}`;

  const share = () => {
    // the clipboard needs https (or localhost); otherwise show the link to copy by hand
    if (!navigator.clipboard) return setShared("show");
    navigator.clipboard.writeText(link).then(
      () => setShared("copied"),
      () => setShared("show"),
    );
  };

  const remove = () =>
    apiSend("DELETE", `/api/bookmarks/${b.id}`).then(
      () => {
        onChanged();
        navigate("/recordings?view=bookmarks", true);
      },
      (e) => setError(`Not deleted: ${e.message ?? e}`),
    );

  return (
    <article class="bookmark" aria-labelledby="bookmark-title">
      <div class="bookmark-head">
        {renaming ? (
          <RenameForm b={b} onDone={() => setRenaming(false)} onChanged={onChanged} />
        ) : (
          <h2 id="bookmark-title">{b.name}</h2>
        )}
        <p class="secondary">
          {dayFmt.format(when)}, {clockFmt.format(when)}
          {b.freq && `, ${b.freq} MHz`}
        </p>
      </div>
      <div class="bookmark-actions">
        {b.recording && (
          <button type="button" class="primary" onClick={() => playBookmark(player, b)}>
            Play from the bookmark
          </button>
        )}
        <button type="button" onClick={share}>
          {shared === "copied" ? "Link copied" : "Copy link"}
        </button>
        {!renaming && (
          <button type="button" onClick={() => setRenaming(true)}>
            Rename
          </button>
        )}
        {confirmDelete ? (
          <span class="confirm">
            Delete “{b.name}”?{" "}
            <button type="button" class="danger" onClick={remove}>
              Delete bookmark
            </button>
            <button type="button" onClick={() => setConfirmDelete(false)}>
              Keep it
            </button>
          </span>
        ) : (
          <button type="button" onClick={() => setConfirmDelete(true)}>
            Delete
          </button>
        )}
      </div>
      {shared === "show" && (
        <label class="share-link">
          Link to this bookmark
          <input readOnly value={link} ref={(el) => el?.select()} onFocus={(e) => e.currentTarget.select()} />
        </label>
      )}
      {error && <p class="form-error">{error}</p>}
      {b.recording ? (
        <ol class="hours">
          <HourRow rec={b.recording} player={player} open={focus} onOpen={setFocus} marks={sameHour} />
        </ol>
      ) : (
        <p class="empty">The recording this bookmark points into is no longer there, so it cannot be played.</p>
      )}
    </article>
  );
}

function RenameForm({ b, onDone, onChanged }: { b: Bookmark; onDone: () => void; onChanged: () => void }) {
  const [name, setName] = useState(b.name);
  const [error, setError] = useState<string | null>(null);
  const input = useRef<HTMLInputElement>(null);
  useEffect(() => input.current?.select(), []);
  return (
    <form
      class="rename"
      onSubmit={(e) => {
        e.preventDefault();
        if (!name.trim()) return;
        apiSend("PATCH", `/api/bookmarks/${b.id}`, { name: name.trim() }).then(
          () => {
            onChanged();
            onDone();
          },
          (e) => setError(`Not renamed: ${e.message ?? e}`),
        );
      }}
      onKeyDown={(e) => e.key === "Escape" && onDone()}
    >
      <label class="visually-hidden" for="rename-input">
        Name
      </label>
      <input id="rename-input" ref={input} value={name} maxLength={100} onInput={(e) => setName(e.currentTarget.value)} />
      <button type="submit" class="primary" disabled={!name.trim()}>
        Save name
      </button>
      <button type="button" onClick={onDone}>
        Cancel
      </button>
      {error && <p class="form-error">{error}</p>}
    </form>
  );
}
