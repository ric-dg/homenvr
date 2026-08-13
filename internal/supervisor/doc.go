// Package supervisor owns the long-running child processes: ffmpeg capture
// instances per camera, the go2rtc media server, and the optional mic daemon.
//
// Responsibilities (mirroring v1's run.ps1 + watchdog.py):
//   - start/stop/restart children on config hot-reload,
//   - per-service binary resolution (PATH or explicit tools.* paths),
//   - crash detection and restart with backoff,
//   - clean shutdown ordering on stop.
package supervisor
