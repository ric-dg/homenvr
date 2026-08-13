// Package watchdog supervises system health and alerting.
//
// It probes each component (the go2rtc API, each camera's stream producers,
// and every enabled mic's TCP ports) and, after a check has been failing
// continuously for monitor.alert_after_sec, logs an ALERT and optionally
// pushes to ntfy.sh. Mirrors v1's watchdog.py, including the per-check
// down/alert/recover state machine and hot-reloaded monitor.* settings.
package watchdog
