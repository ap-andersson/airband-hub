# airband-hub: design and plan

The design notes behind airband-hub: why it exists, the decisions made, what was found on the
way, and what comes next. It is updated as work lands.

## Goal

Replace a typical RTLSDR-Airband setup (Icecast for the stream, a scraper for the stats file,
a web server for the recordings, all around one big multi-process container) with two
containers:

- **airband**: `rtl_airband` only, in a small single-process image that is
  compiled at build time.
- **airband-hub**: one Go service that:
  - relays the live MP3 stream so VLC, browsers and phones can still play it
  - shows a dashboard with live stats
  - has a recordings browser with an Audacity-style waveform view
  - may handle config editing later

Related repos:

- [RTLSDR-Airband fork](https://github.com/ap-andersson/RTLSDR-Airband): the radio software itself
- [docker-rtlsdrairband fork](https://github.com/ap-andersson/docker-rtlsdrairband): the all-in-one s6 container (rtl_airband + Icecast) this replaces

## Decisions

| # | Decision | Why |
|---|---|---|
| D1 | Separate repo `airband-hub` instead of restructuring docker-rtlsdrairband | That repo follows sdr-enthusiasts upstream and is designed around s6 |
| D2 | Hub written in Go | Single static arm64 binary, small image, a natural fit for stream fan-out |
| D3 | Drop Icecast. The hub implements the Icecast *source* protocol, so rtl_airband's `icecast` output works unchanged. | Keeps VLC compatibility without Icecast's XML config and admin UI |
| D4 | rtl_airband serves `GET /metrics` itself, built to upstream quality | Removes the stats-file + ncat scraper. Could be sent upstream. |
| D5 | Keep the existing metric names (e.g. `channel_squelch_counter`) | Renaming them breaks dashboards. Not worth it. |
| D6 | No JSON status endpoint for now | The hub gets all it needs from the stream push, the metadata updates and `/metrics` (see Data flow) |
| D7 | Add a `channel_squelch_open` gauge (0/1 per frequency) | The only live value the hub can't get any other way |
| D8 | Recordings are browsed through a waveform view with automatic transmission markers | That is how one finds transmissions by hand in Audacity |
| D9 | The UI is Preact + TypeScript + Vite, embedded in the Go binary | Several pages ahead (config editing), but still one container and one port |
| D10 | MP3 decoding with minimp3 (C, vendored) via cgo | Pure-Go decoders cannot read 8 kHz MPEG-2.5, which is what rtl_airband writes |
| D11 | rtl_airband is built with NFM off | NFM support also doubles the audio rate from 8 to 16 kHz |
| D12 | Applying config changes restarts rtl_airband: through a restricted Docker socket if one is provided, otherwise the user is asked to | rtl_airband has no config reload |
| D13 | The hub's own data lives in one SQLite file, `hub.db` in `HUB_DATA_DIR`, through the pure-Go `modernc.org/sqlite` | No database server to run, and no C to compile for it. It costs a few MB of image size. The schema is versioned with `PRAGMA user_version`. |

## Architecture

No Icecast server anywhere. The only thing that keeps the name is rtl_airband's
output *type* `icecast`. That output is just the client half of a simple HTTP
streaming protocol (via libshout), and the hub implements the server half.

```
RTL-SDR ──USB──> airband (rtl_airband)
                   │  stream push: MP3 over HTTP + "now on frequency X" metadata
                   │      (rtl_airband output type "icecast", pointed at the hub)
                   │  hourly MP3 files -> ./recording (shared volume, read-only for the hub)
                   │  GET /metrics (Prometheus text)
                   ▼
                 airband-hub (Go)
                   ├─ GET /<mount>.mp3      live stream for VLC / browser <audio>
                   ├─ /                     dashboard UI
                   ├─ /api/...              stats, recordings, waveform peaks
                   └─ /metrics?             (maybe: the hub's own metrics)
Prometheus ── scrapes airband /metrics directly
```

### Data flow: where the hub gets what

| What the hub needs | Source |
|---|---|
| Live audio | Stream push from rtl_airband's `icecast`-type output |
| Active scan frequency | Metadata updates (`/admin/metadata?mode=updinfo&song=...`), already delayed by `shout_metadata_delay` to match the audio |
| Levels, noise floor, squelch, SNR, airtime | Reads airband `/metrics` every ~1-2 s and parses it with `prometheus/common/expfmt` |
| Squelch open now | `channel_squelch_open` (D7) |
| Recording waveforms and transmission markers | Peaks computed from the MP3 files, cached in the hub's own data volume |

## Status

**Done**
- **`/metrics` in rtl_airband** ([RTLSDR-Airband fork](https://github.com/ap-andersson/RTLSDR-Airband), branch `dev/metrics_endpoint`):
  - `metrics_listen = "host:port";` serves the same Prometheus text as the stats file, at `GET /metrics`.
  - It is a hand-rolled HTTP server on its own thread.
  - The response is rendered by the output thread through a request/publish handoff, so the HTTP thread never reads radio data.
  - It adds the `channel_squelch_open` gauge and writes non-finite values as `NaN`/`+Inf`/`-Inf`.
  - Unit and system tests; the system tests parse the output with `prometheus_client`.
- **Lean rtl_airband image** (branch `dev/docker_build_options`):
  - the fork's Alpine Dockerfile with an `NFM` build argument and a BuildKit cache mount for the build tree
  - 32 MB, runs only `rtl_airband` under tini
  - a rebuild after a source change takes seconds
- **The stack:** `deploy/compose.yml` runs rtl-airband and the hub, and `scripts/deploy.sh` builds both on the host.
  - rtl-airband sits behind a compose profile, so its image can be built while another container still owns the SDR.
  - `compose.legacy.yml` lets the hub run next to an older container during a migration.
- **Live stream:**
  - Icecast source protocol, with the frequency titles
  - fan-out to listeners, who start at the live edge
  - slow listeners are dropped
  - a grace period so listeners survive an rtl_airband restart
  - ICY metadata for VLC
  - an Icecast-compatible `/status-json.xsl`, for players such as tar1090's
- **Dashboard:**
  - a strip per frequency with a level meter (noise floor, signal, squelch level); the strip lights up while squelch is open
  - recent transmissions, with exact length and SNR range, derived from the metrics histograms
  - receiver health
- **Recordings browser:**
  - a waveform per recorded hour, with transmission markers
  - click to play from an exact frame
  - a 5-minute detail view
  - previous/next transmission and skip silence
  - waveforms cached on disk and analysed in the background
  - days grouped in the viewer's time zone
- **Bookmarks** (SQLite, D13):
  - "Add bookmark" in the player saves the moment with a name. Inside a transmission it points at the transmission's start.
  - a Bookmarks view on the recordings page, with flags on the waveforms
  - every bookmark has its own address (`/recordings?bookmark=<id>`), which the page always shows, plus a Copy link button
  - rename and delete
  - ids are never reused, so an old link cannot open a different bookmark
  - bookmarks store the final file name, so one made in the hour being recorded still works after the `.tmp` is renamed

- **One player for the whole UI:** live and recordings share the bar at the bottom, with a loading indicator and volume, and playback carries on between pages. Live audio is decoded in the page (see Findings) and runs about 0.5 s behind the receiver.

**Next**
- [ ] **Access control.** Today everything is open. Before the hub is reachable from the internet, the recordings and (later) config editing need a login, or the hub needs a way to limit them to the LAN.
- [x] Publish images: GitHub Actions builds both images for amd64 and arm64 on native runners and publishes them to ghcr. The fork's upstream workflows are kept but only run by hand. `deploy.sh` pulls the images, and `--build` still builds on the host.
- [ ] Store transmission history in SQLite too (`hub.db`, D13), so the dashboard's history survives restarts and can go back X days.
- [ ] Pull requests for the two RTLSDR-Airband branches (`dev/metrics_endpoint`, `dev/docker_build_options`, both pushed), first to the fork and maybe upstream later.
- [ ] Config editing (the end goal): edit `rtl_airband.conf`, starting with the scan list, in the hub. Back it up before writing, then restart (D12).
- [ ] Check the dashboard at phone width and in light mode.

**Ideas**
- Set the title from the metrics when squelch opens, so it survives hub restarts and updates on every transmission
- Continue playback into the next hour
- Zoom the waveform by dragging
- A whole-day "transmissions only" playlist
- A low-latency path: `udp_stream` PCM sent to the browser over WebSocket
- The hub's own metrics (listeners, relay health)

## Findings

### Stream push protocol (captured 2026-11-18)
Captured locally from rtl_airband using libshout 2.4.6, the same version as on the Pi.
It is the Icecast 2 "SOURCE" protocol, **not PUT**:

1. `SOURCE /<mount> HTTP/1.0` arrives first **without** `Authorization` and with `Content-Length: 0`.
   Real Icecast answers `401` with `WWW-Authenticate: Basic`.
2. libshout reconnects with `Authorization: Basic base64(user:pass)` (user `source`),
   then sends the stream info headers:
   - `Content-Type: audio/mpeg`
   - `ice-name`, `ice-genre`, `ice-description`, `ice-public`
   - `ice-audio-info: samplerate=8000;channels=1`
   - `Prefer: return=minimal`

   No `Expect: 100-continue`.
3. After a `200` response, the request body is the raw MP3 stream with no chunking and no
   length, until the connection closes. At CBR 48 kbps that is about 6 KB/s, and it starts with MPEG-2 Layer III frames (`ffe3`).
4. Metadata arrives on separate connections:
   `GET /admin/metadata?mode=updinfo&charset=UTF-8&mount=%2ftest%2emp3&song=<label> HTTP/1.1`.
   It is also tried without auth first and then with Basic auth.
   The expected reply is `200` with an `<iceresponse>` XML body.
   It is sent `shout_metadata_delay` (3 s) after the retune, so it lines up with the audio.

### Live playback in the browser (measured 2026-11-19)
- Given the stream URL, Chrome buffers about 130 KB before it starts, which is about 22 s at 6 KB/s. It then plays from the start of that buffer, so it stays about 20.5 s behind for the whole session. Firefox stays about 5 s behind.
- Media Source Extensions fix this in Chrome, but Firefox does not accept a raw MP3 stream (`audio/mpeg`) there.
- **Decision:** the page reads the stream itself, splits it into frames, decodes them with WebCodecs (`AudioDecoder`, which both browsers support for MP3, 8 kHz included) and schedules them in Web Audio with a 0.4 s cushion (`web/src/liveDecoder.ts`). Measured: about 0.6 s behind in Firefox and Chrome.
- WebCodecs needs a secure context: https or localhost. Over plain http the player falls back to the browser's own buffering.

### Behaviour seen in use
- **Silent after connecting:** rtl_airband connects its outputs before the dongle is running and sends no
  audio for ~10 s. With a 10 s source timeout the hub dropped it and rtl_airband reconnected 10 s
  later, so the default timeout is now 30 s.
- **The title only exists after a transmission:** it is kept in memory, so a hub restart clears it until rtl_airband sends the next one.
  A possible improvement: when the dashboard's metrics show squelch opening on a frequency, set the title from that.
- **The title is the frequency of the latest transmission, not the frequency being scanned.** rtl_airband sends a
  tag only when squelch opens on a different frequency from the last active one
  (`src/rtl_airband.cpp:171`). So it is empty after a restart until the first transmission. The
  dashboard should label it "last active". The `channel_squelch_open` metric shows what is open right now.

### Seeking in the hourly VBR recordings (measured on a real hourly file)
- The file is MPEG-2 Layer III at 8 kHz, with 50,000 frames of 72 ms each and **98% of frames at 8 kbps (silence)**.
- It has a valid Xing header with a seek table. rtl_airband writes it when it closes the file
  (`lame_get_lametag_frame`, `src/output.cpp:410`).
  The current hour's `.tmp` file does not have it yet.
- Seeking by guessing from file size is **up to 34 s off**.
- Seeking through the Xing seek table is still **about 5 s off on average, 12 s at p95 and 15 s at worst**.
  Positions are stored in steps of 1/256 of the file (~14 KB), which is ~14 s of silence.
- **Decision:** the hub keeps an exact frame index per file. It already parses every frame to draw the waveform.
  Playback after a click requests audio starting at a given time; the hub serves it from
  the exact frame, and the UI adds that start time to the player position. This also works for the existing recordings.

## Risks and things to verify

- ~~VBR seeking~~: measured and solved with a frame index (see Findings).
- ~~Icecast source protocol details~~: captured (see Findings).
- **Restarts in the middle of an hour:** the file output uses `append = true`. After a restart, the
  hourly file holds two encoder sessions, and the Xing header may only describe one of them. The frame index
  must not trust the Xing header. Scan the frames instead.
- **Losing LiveATC-style feeding:** a second `icecast` output to an external
  server is still possible if it's ever needed.
- **Security:** the hub has no login yet. Anyone who can reach it can listen, browse and
  download all recordings, add, rename and delete bookmarks, and later edit the config. Do not forward its port to the
  internet until access control exists. `docker.sock` must never be mounted directly; use
  a proxy that only allows restarting the rtl_airband container.
