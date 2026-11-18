# airband-hub plan

Living document. Update it as decisions are made and work lands.

## Goal

Replace the Icecast server, the stats-file scraper and the recordings nginx with
two containers:

- **airband**: `rtl_airband` only, in a small single-process image that is
  compiled at build time.
- **airband-hub**: one Go service that:
  - relays the live MP3 stream so VLC, browsers and phones can still play it
  - shows a dashboard with live stats
  - has a recordings browser with an Audacity-style waveform view
  - may handle config editing later

Related repos:

- [RTLSDR-Airband fork](https://github.com/ap-andersson/RTLSDR-Airband): the radio software itself
- [docker-rtlsdrairband fork](https://github.com/ap-andersson/docker-rtlsdrairband): today's all-in-one s6 container. The README should link back to it.

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
| D8 | Recordings are browsed through a waveform view with automatic transmission markers | This is how it's done by hand in Audacity today |

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

## Current setup on the Pi (as of 2026-11-18)

- One container (`ap-andersson/docker-rtlsdrairband`) running s6 with
  Icecast on :80, rtl_airband, and ncat serving the stats file on :8080.
- A separate `airband-nginx` serves `./recording` on :8081.
- rtl_airband runs one RTL-SDR in scan mode with one channel and 4 frequencies, and two outputs:
  - an icecast mount with `send_scan_freq_tags`, CBR 48 kbps
  - a continuous **hourly** file (`essl_YYYYMMDD_HH.mp3`, dated subdirectories, default VBR, about 3.7 MB/h)
- The current hour is written as `.mp3.tmp` and renamed when the hour is done.
- ~59 GB of recordings since 2025-01.
- Dev mode is on (`RTLSDRAIRBAND_BUILD_PLATFORM=generic` together with a source bind mount).

## Work plan

### Phase 0: catch up with upstream
Done 2026-11-18, merged locally to `main` and not pushed yet.
- [x] RTLSDR-Airband: merge `upstream/main`. It is 1 commit behind: #583, Ubuntu
  26.04 and CMake fails at configure time if a library is missing. It conflicts
  in `CLAUDE.md`, because upstream moved the src notes to `src/CLAUDE.md` and
  added a ci-and-docker skill. Resolve it by moving the fork's Stats/Metrics and
  retune notes into `src/CLAUDE.md`.
- [x] docker-rtlsdrairband: merge `upstream/main`. It is 13 commits behind:
  - Icecast 2.5 `burst-on-connect` fix
  - Debian forky build
  - fail-fast build steps
  - CI bumps

  It conflicts in the two workflow files. Resolve it by taking upstream's newer
  `sdre.yml` pin and keeping the fork's `permissions:` blocks.

### Phase 1: `/metrics` in rtl_airband (RTLSDR-Airband repo, upstream quality)
Implemented 2026-11-18 on branch `dev/metrics_endpoint`. Not committed yet.
- [x] Config: `metrics_listen = "host:port";` (also `"[::1]:9101"`, and `":9101"` for all interfaces with IPv6 dual-stack). The endpoint is off when it's not set. An invalid address or a busy port aborts startup.
- [x] HTTP server (`src/metrics_server.cpp`):
  - hand-rolled, no new dependency, on its own thread
  - one connection at a time
  - `GET`/`HEAD /metrics`, 404 for other paths, 405 with `Allow` for other methods, 400 for a malformed request, 431 above 8 KiB
  - 5 s deadline per request
  - closes connections gracefully so the client never gets a reset instead of the response
- [x] Rendering: the HTTP thread asks the first output thread through `MetricsExchange`. That thread builds the text with `open_memstream()` using the same `write_metrics()` as the stats file, so the HTTP thread never reads radio data. It returns 503 if there's no answer within 2 s.
- [x] `channel_squelch_open` gauge. In scan mode only the tuned frequency can report 1.
- [x] Non-finite values are written as `NaN`/`+Inf`/`-Inf` (printf's `-nan` breaks parsers).
- [x] Tests:
  - 25 new unit tests (`src/test_metrics_server.cpp`, plus non-finite values in `test_metrics.cpp`)
  - 6 new system tests (`system_tests/tests/test_metrics_endpoint.py`): a live scrape parsed with `prometheus_client` and compared to the stats file, plus the config and port-in-use errors
  - all 4 build configurations build, and 157 unit tests and 63 system tests pass
- [x] `CLAUDE.md`/`src/CLAUDE.md` updated, and wiki text drafted.
- [x] Tested on the Pi 2026-11-18 with the real dongle:
  - 200 with the correct content type, 404 and 405 work
  - ~120 ms per scrape, which is the wait for the next output loop
  - 21 metric families, the same as the file
  - `channel_squelch_open` went to 1 on 129.800 during real transmissions
  - Icecast reconnected and the old :8080 scraper still works
- [x] Committed `b305fe9` on `dev/metrics_endpoint` (not pushed).
- [x] the Pi switched 2026-11-18: the host's :8080, Prometheus' existing target, now goes to the native endpoint (container :9101). The ncat scraper is off.
- [ ] Watch it in Grafana. Confirm Prometheus scrapes `/metrics`: the native endpoint returns 404 for any other path.
- [ ] Push and open a PR to the fork's main. Upstream PR later? The docker-rtlsdrairband ncat scraper can then be dropped.
- Notes:
  - `prometheus-client` was added as a system-test dependency.
  - `uv.lock` was regenerated by a newer uv (lockfile revision 3 -> 5), so its diff is bigger than the one new package.

### Phase 2: airband container image
- [ ] Use or adapt the fork's existing multi-stage Alpine `Dockerfile` (tini, rtl-sdr-blog driver) and publish it to ghcr.
- [ ] Work out a replacement for the fast dev loop (source sync and a ~20 s rebuild).

### Phase 3: airband-hub MVP
- [x] Go module skeleton (`github.com/ap-andersson/airband-hub`).
  - Dockerfile: static binary on distroless, 9.4 MB image.
  - README with a link back to docker-rtlsdrairband.
- [x] Icecast source protocol receiver (`internal/icecast`):
  - `SOURCE`/`PUT` with a 401 Basic challenge, then a hijacked raw body
  - 403 when a mount is already in use
  - source idle timeout
  - `/admin/metadata?mode=updinfo` with an `<iceresponse>` reply
  - **first:** captured what libshout sends (see Findings)
- [x] Listener relay (`internal/relay`):
  - `GET`/`HEAD /<mount>` fan-out
  - a burst buffer cut to an MP3 frame start
  - slow listeners are dropped and never block the source
  - a 30 s grace period, so listeners survive an rtl_airband restart
- [x] `GET /api/mounts` (mount, title = active frequency, listeners, source state) and `/healthz`.
- [x] Tests:
  - unit tests replay the captured libshout session over TCP, all passing with `-race`
  - end-to-end with a real rtl_airband in the devcontainer: login accepted, burst after 0.01 s starting at `ffe3`, frequency title received
- [ ] Run on the Pi next to Icecast (a second `icecast` output in rtl_airband pointing at the hub). Needs an arm64 image.
- [ ] Optional ICY metadata so VLC shows the current frequency.
- [ ] Read `/metrics` and show a live dashboard: active frequency, levels, squelch, transmissions per hour.
- [ ] Recordings browser:
  - list by date
  - exact frame index and waveform peaks per file (decode in Go), cached and backfilled in the background
  - wavesurfer.js view with zoom
  - click to play: the hub serves audio from the exact frame (see Findings)
  - automatic transmission markers worked out from the peaks
  - show the growing current-hour `.tmp` file
- [ ] Replace the `airband-nginx` container.

### Phase 4: later / maybe
- [ ] Config editing:
  - write the conf and restart the airband container through a locked-down docker-socket-proxy (restart only)
  - auth
  - back up the config before writing
- [ ] Optional low-latency path: `udp_stream` PCM sent to the browser over WebSocket.
- [ ] Optional split per-transmission recordings (`split_on_transmission` + `include_freq`).

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

### Seeking in the hourly VBR recordings (one real file, 2026-11-18 16:00)
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
- **Security:** the dashboard and any config/restart feature need auth if
  they're ever reachable outside the LAN. `docker.sock` must never be mounted
  directly.

## Open questions

- Should the hub re-export its own metrics (listeners, relay health)?
