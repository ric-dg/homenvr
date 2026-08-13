// Package sleepctx provides a context-aware sleep used by the long-running
// per-camera loops so they exit promptly on shutdown.
package sleepctx

import (
	"context"
	"time"
)

// Sleep waits for d or until ctx is cancelled. It returns false when the
// context was cancelled before the sleep completed.
func Sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
