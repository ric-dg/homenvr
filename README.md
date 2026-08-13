# HomeNVR v2 (scaffold)

Cross-platform rewrite of [HomeNVR v1](https://github.com/ric-dg/homenvr-v1)
in Go. v1 (Python, v0.1.1) is complete and kept as the reference;
v2 starts clean.

**Status: scaffold only.** No features implemented yet. This repo documents
the intended architecture and conventions; nothing here runs a camera.

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
internal/supervisor/ child process management (ffmpeg, go2rtc, mic)
internal/motion/     motion/sound gating for event recordings
internal/watchdog/   health probes + alerts (ntfy)
internal/web/        control panel (embedded HTTP server)
internal/retention/  rolling deletion by retain_hours
```

## Build & test

```powershell
go build ./...
go vet ./...
go test ./...
go build -o homenvrd.exe ./cmd/homenvrd
```

Cross-compile (no CGO):

```powershell
$env:GOOS="linux"; $env:GOARCH="arm64"; go build -o homenvrd ./cmd/homenvrd
```

## Conventions

- Stdlib-only for now (no third-party modules until required).
- `internal/` packages are not importable outside the module.
- Live config and logs are never committed (see `.gitignore`).
