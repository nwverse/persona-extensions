package main

import (
	"context"
	"time"
)

// Desktop authorization is 10 minutes idle, at most 12 hours, and revoked by App locking.
// Refresh only after an interactive foreground call has succeeded in this resident process. Never keep
// retrying a failed/locked session or try to defeat the hard limit. whoami's identity output is discarded.
const keepaliveEvery = 5 * time.Minute

func (n *nodeApp) noteAuthorization(now time.Time) {
	if n.keepaliveStarted.IsZero() || n.keepaliveBlocked || now.Sub(n.keepaliveStarted) >= 12*time.Hour {
		n.keepaliveStarted = now
	}
	n.keepaliveAt = now
	n.keepaliveBlocked = false
}
func (n *nodeApp) keepaliveDue(now time.Time) bool {
	return !n.keepaliveBlocked && !n.keepaliveStarted.IsZero() && now.Sub(n.keepaliveAt) >= keepaliveEvery && now.Sub(n.keepaliveStarted) < 12*time.Hour-keepaliveEvery
}
func (n *nodeApp) keepalive(ctx context.Context) {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tick.C:
			n.mu.Lock()
			if n.keepaliveDue(now) {
				c, cancel := context.WithTimeout(ctx, 9*time.Second)
				out, err := n.run(c, "whoami", "--format", "json")
				clear(out)
				cancel()
				if err != nil {
					n.keepaliveBlocked = true
				} else {
					n.keepaliveAt = now
				}
			}
			n.mu.Unlock()
		}
	}
}
