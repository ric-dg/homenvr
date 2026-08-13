// Package watchdog supervises the system health and alerting.
//
// It probes each component (go2rtc API, per-camera motion levels, supervisor
// children) and, when enabled, sends notifications (ntfy) on failure.
// Mirrors v1's watchdog.py including an independent go2rtc API probe.
package watchdog
