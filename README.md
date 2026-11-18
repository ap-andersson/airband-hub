# airband-hub

A small Go service that sits next to [RTLSDR-Airband](https://github.com/ap-andersson/RTLSDR-Airband)
and replaces the Icecast server, the stats-file scraper and the recordings web server
from [docker-rtlsdrairband](https://github.com/ap-andersson/docker-rtlsdrairband) with one
container: a live stream that still plays in VLC and browsers, a dashboard, and a recordings
browser with a waveform view.

**Status: early work in progress.** Today it relays the live stream. See [PLAN.md](PLAN.md) for
the design, decisions and what comes next.

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
| `GET /<mount>` | Live MP3 stream, e.g. `http://host:8000/essl.mp3` in VLC or a browser |
| `GET /api/mounts` | JSON: mounts, current frequency (title), listeners, source state |
| `GET /healthz` | Liveness |

## Configuration

| Variable | Default | |
|---|---|---|
| `HUB_SOURCE_PASSWORD` | (required) | Password rtl_airband's icecast output uses |
| `HUB_SOURCE_USER` | `source` | Username for it |
| `HUB_LISTEN` | `:8000` | Listen address |
| `HUB_BURST_BYTES` | `24576` | Recent audio sent to a new listener at once, ~4 s at 48 kbps |
| `HUB_SOURCE_TIMEOUT` | `10s` | Drop a source that sends nothing this long |
| `HUB_MOUNT_GRACE` | `30s` | Keep listeners connected this long while the source reconnects |
| `HUB_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |

## Development

```bash
go test -race ./...
go build -o bin/airband-hub ./cmd/airband-hub
docker build -t airband-hub .
```
