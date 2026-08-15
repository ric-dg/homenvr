# HomeNVR v2

Cross-platform rewrite of [HomeNVR v1](https://github.com/ric-dg/homenvr-v1)
in Go. v1 (Python, v0.1.1) is complete and kept as the reference.

**Status: feature-complete.** A single static Go daemon (`homenvrd`) replaces
all v1 Python glue: config, supervisor (go2rtc + per-camera ffmpeg/mic
processes), motion/sound gating for event recordings, continuous/combined
recording, retention, watchdog, and an embedded control panel with the
recordings browser. `-gen-yaml` output is byte-identical to v1's go2rtc.yaml,
and it was smoke-tested live (go2rtc spawn, status/recordings/shutdown API,
clean exit). ffmpeg and go2rtc remain external tools; the mic capture,
motion detector and audio capture are native Go.

## Why a rewrite

- One static binary per platform, no Python runtime to install.
- Same config + daemon surface on Windows (WinSW), Linux (systemd),
  macOS (launchd).
- Typed supervisor/watchdog instead of script glue.

ffmpeg and go2rtc stay for capture (they do the real work); v2 reimplements
the glue: config, supervisor, watchdog, motion/sound gating, retention,
control panel, and the per-OS service layer.

## Layout

```
cmd/homenvrd/        entry point (CLI, service mode, version)
internal/config/     JSONC config load/validate/hot-reload
internal/proc/       process tree management + graceful kill (taskkill)
internal/sleepctx/   cancellable timers for supervisor loops
internal/supervisor/ child process management (ffmpeg, go2rtc, mic)
internal/mic/        audio capture + level gating (native Go, no deps)
internal/motion/     motion/sound gating for event recordings
internal/recorder/   event / continuous / combined recording loops
internal/retention/  rolling deletion by retain_hours
internal/watchdog/   health probes + alerts (ntfy)
internal/web/        control panel (embedded HTTP server)
packaging/service/   WinSW service install + shared helper module
scripts/             dev-ops helpers (status/probe/deploy/restart/...)
```

On Windows the mic is captured natively by the daemon: WASAPI by default,
with a WDM-KS (kernel streaming) fallback when the audio policy layer exposes
no endpoint for the configured device (see `mic.backend` in the config:
`"ks"`, `"wasapi"`, or `""` for auto). The Brio-class webcam mic is never a
DirectShow device, so the old ffmpeg `-f dshow` path cannot open it.

## Run

```powershell
cp config.example.jsonc config.jsonc   # edit paths/cameras
.\homenvrd.exe -config config.jsonc -yaml go2rtc.yaml
```

- Control panel: `http://localhost:8080` (config `web.port`, `web.bind`).
- Live view + go2rtc web UI: `http://localhost:1984` (config `go2rtc.api_port`).
- Recordings: `GET /api/recordings`, playback via
  `GET /api/recordings/{camera}[/combined]/{file}` (Range-supported MP4).
- Stop gracefully: `POST /api/shutdown` (panel "Shutdown" button).

### Web-only operations (no desktop, no UAC)

The panel covers service administration, because the daemon runs as the
service account - every operation below works from any browser, fully
headless:

| Action | Endpoint | Panel |
|---|---|---|
| Config edit + hot reload | `GET/PUT /api/config` | Config tab |
| Restart service | `POST /api/restart` | Status → Restart service |
| Run retention now | `POST /api/retention/run` | Status → Run retention |
| Log tails | `GET /api/logs?name=service\|alert\|go2rtc\|go2rtc.err` | Logs tab |
| Stop service | `POST /api/shutdown` | Status |
| Self-update binary | `POST /api/update` (raw exe body) | Status → Upload & restart |

Restart works because the daemon exits with a reserved non-zero code that
WinSW's `onfailure action="restart"` turns into a restart. Self-update stages
the uploaded exe, spawns a detached PowerShell helper, then exits; the helper
swaps the binary and starts the service (`update.log` next to the exe records
its progress). Set `web.bind` to `0.0.0.0` or a LAN/VPN IP to reach the panel
remotely - keep `127.0.0.1` unless the network is trusted, since the panel
can restart and replace the daemon.

## Windows service

Requires [WinSW](https://github.com/winsw/winsw/releases) (any recent 2.x).
From an elevated shell, with `go` available in PATH:

```powershell
.\packaging\service\install-service.ps1 -WinSW C:\path\to\WinSW-x64.exe -ServiceDir C:\Tools\homenvr
.\packaging\service\homenvr-on.ps1 -Confirm
.\packaging\service\homenvr-off.ps1 -Confirm
```

`install-service.ps1` builds `homenvrd.exe` and `homenvrd-tray.exe`, stages
them under `-ServiceDir` with the WinSW binary and rendered XML, and registers
service `homenvrd` (Automatic start, restart on failure, rolled logs). By
default it also creates a `HomeNVR Tray.lnk` in the startup folder and on the
desktop pointing at the tray; tick them off with
`-StartupShortcut:$false`/`-DesktopShortcut:$false`. The scripts derive their
paths dynamically - install dir, log dir and ports all come from the
installed service and `config.jsonc`, so nothing is host-specific.
`-ServiceDir` is only required for a fresh install; an existing service is
relocated automatically. `-NoBuild` skips the build, `-Uninstall` removes the
service and the tray shortcuts, and `-ConfigPath`/`-YAMLPath` override where
config and the generated go2rtc.yaml are staged. The daemon runs under
LocalSystem and resolves ffmpeg/ffprobe/go2rtc by bundled copy, PATH, then
scoop, so no service-specific environment is required.

### Dev-ops helpers

`scripts/homenvr.ps1` wraps the service scripts and adds diagnostics. Run it
from a normal prompt; the commands that touch the service elevate themselves
(accept the UAC prompt):

```powershell
.\scripts\homenvr.ps1 status            # service state + process tree
.\scripts\homenvr.ps1 probe             # + log tails + go2rtc /api/streams
.\scripts\homenvr.ps1 det-repro         # run the detector's ffmpeg pipe standalone
.\scripts\homenvr.ps1 start | stop      # start/stop the service
.\scripts\homenvr.ps1 restart           # stop, kill orphans, start
.\scripts\homenvr.ps1 deploy            # build, swap binary, restart
```

`det-repro` runs the motion detector's exact ffmpeg command with stderr
captured (the daemon discards it) and reports frames per second - it tells
you whether a "detector stream lost" problem is the RTSP/go2rtc feed or the
daemon's frame reading.

## Build & test

```powershell
go build ./...
go vet ./...
go test ./...
go build -o homenvrd.exe ./cmd/homenvrd
```

Useful right now:

```powershell
homenvrd.exe -dump-config -config path\to\config.jsonc   # effective config as JSON
homenvrd.exe -validate-config -config path\to\config.jsonc
homenvrd.exe -gen-yaml -config path\to\config.jsonc      # go2rtc.yaml output
```

Cross-compile (no CGO):

```powershell
$env:GOOS="linux"; $env:GOARCH="arm64"; go build -o homenvrd ./cmd/homenvrd
```

`go build ./...` passes on Windows, Linux and macOS; the optional tray helper
(`cmd/homenvrd-tray`) is Windows-only behind a build tag and is skipped on
other platforms.

## Roadmap

Kept deliberately cheap and settings-gated; nothing here runs unless you turn
it on.

- **Federation** - multiple HomeNVR hosts share a timeline and clips. The
  panel API is the seam: cross-host config is untrusted by design, so
  federation peers would be read-only (status + recordings, no admin).
- **Tray on Linux/macOS** - the current tray is Windows-only because
  `DETACHED_PROCESS` and the service model are. Native `libappindicator`
  (Linux) and a menu-bar status item (macOS) can share the same panel-API
  actions once those platforms have a deployed daemon.
- **Pre-roll buffer** - a short pre-event buffer via libx264 flat .ts or the
  recorder's ring buffer. Breaking change: event and live would share one
  timeline clock, so multi-camera timestamps interleave.
- **Detection zones + object detection** - would follow lightNVR, but object
  detection (ONNX) conflicts with the stdlib-only + efficiency goals, so it
  stays deferred unless the requirement appears.

## Conventions

- Stdlib-only for now (no third-party modules until required).
- `internal/` packages are not importable outside the module.
- Live config and logs are never committed (see `.gitignore`).
