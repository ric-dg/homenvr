// Package supervisor owns the long-running child processes: go2rtc (whose
// exec streams pull in the per-camera ffmpeg capture) and, in later
// increments, the feed processes for mic capture, motion gating and event
// recording.
//
// Responsibilities (mirroring v1 run.ps1):
//   - generate go2rtc.yaml from the effective config,
//   - restart go2rtc when the relevant config subset changes (GenHash),
//   - crash detection and restart,
//   - log rotation for managed logs,
//   - clean shutdown ordering on stop.
package supervisor
