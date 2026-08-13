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
packaging/service/   WinSW service install + on/off scripts
```

## Run

```powershell
cp config.example.jsonc config.jsonc   # edit paths/cameras
.\homenvrd.exe -config config.jsonc -yaml go2rtc.yaml
```

- Control panel: `http://localhost:8080` (config `web.port`).
- Live view + go2rtc web UI: `http://localhost:1984` (config `go2rtc.api_port`).
- Recordings: `GET /api/recordings`, playback via
  `GET /api/recordings/{camera}[/combined]/{file}` (Range-supported MP4).
- Stop gracefully: `POST /api/shutdown` (panel "Shutdown" button).

## Windows service

Requires [WinSW](https://github.com/winsw/winsw/releases) (any recent 2.x).
From an elevated shell, with `go` available in PATH:

```powershell
.\packaging\service\install-service.ps1 -WinSW C:\path\to\WinSW-x64.exe
.\packaging\service\homenvr-on.ps1 -Confirm
.\packaging\service\homenvr-off.ps1 -Confirm
```

`install-service.ps1` builds `homenvrd.exe`, stages it under
`C:\Tools\homenvr` with the WinSW binary and rendered XML, and registers
service `homenvrd` (Automatic start, restart on failure, rolled logs).
Pass `-ServiceDir`, `-ConfigPath`, `-YAMLPath` to relocate; `-NoBuild` to
skip the build; `-Uninstall` to remove the service. The daemon runs under
LocalSystem and resolves ffmpeg/ffprobe/go2rtc by bundled copy, PATH, then
scoop, so no service-specific environment is required.

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

## Conventions

- Stdlib-only for now (no third-party modules until required).
- `internal/` packages are not importable outside the module.
- Live config and logs are never committed (see `.gitignore`).
