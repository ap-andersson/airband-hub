# airband-hub

A small Go service that sits next to [RTLSDR-Airband](https://github.com/ap-andersson/RTLSDR-Airband)
and replaces the Icecast server, the stats-file scraper and the recordings web server
from [docker-rtlsdrairband](https://github.com/ap-andersson/docker-rtlsdrairband) with one
container: a live stream that still plays in VLC and browsers, a dashboard, and a recordings
browser with a waveform view.

**Status: early work in progress.** Today it relays the live stream, has a live dashboard
(levels and squelch per frequency, recent transmissions, receiver health) and a recordings
browser (a waveform per recorded hour, transmissions found automatically, play from any point). See [PLAN.md](PLAN.md)
for the design, decisions and what comes next.

## How it works

rtl_airband's `icecast` output is pointed at the hub instead of an Icecast server. The hub
speaks the source side of the Icecast protocol (`SOURCE`/`PUT` with Basic auth, plus
`/admin/metadata` updates), so rtl_airband needs no changes.

```
outputs: (
  {
    type = "icecast";
    server = "airband-hub";      # the hub's hostname
    port = 8000;
    mountpoint = "essl.mp3";
    username = "source";
    password = "change-me";      # HUB_SOURCE_PASSWORD
    send_scan_freq_tags = true;  # the hub shows the active scan frequency
  }
);
```

| Endpoint | What |
|---|---|
| `GET /` | The dashboard |
| `GET /<mount>` | Live MP3 stream, e.g. `http://host:8000/essl.mp3` in VLC or a browser. Players that send `Icy-MetaData: 1` (VLC) get ICY metadata, so they show the frequency of the latest transmission. |
| `GET /api/live` | JSON snapshot behind the dashboard; `/api/live/stream` pushes it every second (server-sent events) |
| `GET /api/recordings/...` | Days, recordings, waveforms, audio from an exact time, downloads (see `internal/recordings/handlers.go`) |
| `GET /api/mounts` | JSON: mounts, current frequency (title), listeners, source state |
| `GET /healthz` | Liveness |

## Configuration

| Variable | Default | |
|---|---|---|
| `HUB_SOURCE_PASSWORD` | (required) | Password rtl_airband's icecast output uses |
| `HUB_SOURCE_USER` | `source` | Username for it |
| `HUB_LISTEN` | `:8000` | Listen address |
| `HUB_BURST_BYTES` | `0` | Recent audio sent to a new listener at once. It makes playback start sooner, but the player then stays that far behind live: 24576 is ~4 s at 48 kbps. 0 matches an Icecast with `burst-size 0`. |
| `HUB_SOURCE_TIMEOUT` | `30s` | Drop a source that sends nothing this long. rtl_airband is silent for ~10 s after connecting while the dongle starts. |
| `HUB_MOUNT_GRACE` | `30s` | Keep listeners connected this long while the source reconnects |
| `HUB_METRICS_URL` | (unset) | rtl_airband's `metrics_listen` endpoint, e.g. `http://rtlsdrairband:9101/metrics`. Without it the dashboard shows only the stream. |
| `HUB_METRICS_INTERVAL` | `1s` | How often to read it |
| `HUB_RECORDINGS_DIR` | (unset) | rtl_airband's file output folder, mounted read-only. Without it there is no recordings page. |
| `HUB_RECORDINGS_LOCALTIME` | `false` | `true` if rtl_airband runs with `localtime = true`, so file names are in `TZ` instead of UTC |
| `HUB_DATA_DIR` | (unset) | Writable folder for the waveform cache (about 6 KB per recorded hour). Without it waveforms are only kept in memory and older recordings are not analysed ahead of time. |
| `HUB_BACKFILL_PAUSE` | `1s` | Pause between analysing older recordings in the background |
| `HUB_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |

## Deploying to a host without a registry

`deploy/compose.yml` is the whole stack: **rtl-airband** (built from an
[RTLSDR-Airband](https://github.com/ap-andersson/RTLSDR-Airband) checkout; only it touches the
SDR) and **airband-hub**. `scripts/deploy.sh` syncs this repository and that checkout to a host
over ssh, builds both images there and restarts the stack:

```bash
cp deploy.env.example deploy.env   # once: DEPLOY_HOST (ssh target), DEPLOY_DIR, AIRBAND_SRC
scripts/deploy.sh                  # sync, build on the host, restart
scripts/deploy.sh --logs           # the same, then follow the logs
```

On the first deploy it creates `DEPLOY_DIR/.env` (mode 600) with a random
`HUB_SOURCE_PASSWORD`; rtl_airband's `icecast` output to the hub uses that password with
`server = "airband-hub"`, `port = 8000`. Other settings in the same file:

| Setting | Default | |
|---|---|---|
| `COMPOSE_PROFILES` | (unset) | `airband` starts rtl-airband. Leave it unset while another container owns the SDR; the image is still built. |
| `AIRBAND_CONFIG` | `./rtl_airband.conf` | rtl_airband's config on the host. It needs `metrics_listen = "0.0.0.0:9101";`. |
| `HUB_RECORDINGS_HOST_DIR` | `./recordings` | Where the config's file outputs write; mounted at `/recording` for rtl_airband and read-only for the hub |
| `HUB_HOST_PORT` | `8090` | Host port for the hub (stream, dashboard) |
| `AIRBAND_METRICS_HOST_PORT` | `8080` | Host port for rtl_airband's `/metrics`, for Prometheus |
| `HUB_UID`, `HUB_GID` | `1000` | Owner of `data/`, the hub's waveform cache |
| `COMPOSE_FILE` | `compose.yml` | `compose.yml:compose.legacy.yml` lets the hub reach an older rtl_airband container's network (`AIRBAND_NETWORK`, default `rtlsdr-airband_default`) |
| `HUB_*` | | Any hub setting from the table above |

rtl-airband runs as root, because the USB device nodes are `root:root 0664`, and is built
with `NFM=FALSE`, since NFM support would also double the audio rate to 16 kHz.
Rebuilding after a source change takes seconds: the build tree is cached between builds.

## Recordings

The recordings page reads rtl_airband's `file` outputs (`<template>_YYYYMMDD_HH.mp3` hourly, or
the split-on-transmission names). Each file is decoded once with
[minimp3](https://github.com/lieff/minimp3) (vendored, public domain; it handles the 8 kHz
MPEG-2.5 files that pure-Go decoders do not) into a peak envelope, and stretches with sound
become transmission markers. Playback starts at an exact MP3 frame from a frame index, because
seeking through the files' own VBR seek table lands 5 to 15 s off.

With `HUB_DATA_DIR` set, every finished recording is analysed in the background, newest first.
On a Raspberry Pi 4 that is about 40 hours of recordings a minute on one core.

## Development

The UI is Preact + TypeScript + Vite in `web/`. It is built into `web/dist` and embedded in
the Go binary, so build it before the binary:

```bash
(cd web && npm ci && npm run build)
go test -race ./...
go build -o bin/airband-hub ./cmd/airband-hub   # needs a C compiler (cgo, for minimp3)
docker build -t airband-hub .
```

For UI work with hot reload, run `npm run dev` in `web/`. It forwards the API and the
stream to a running hub, set with `HUB_URL` (default `http://localhost:8000`), e.g.
`HUB_URL=http://raspberrypi.local:8090 npm run dev`.
