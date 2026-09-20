package monitor

import (
	"context"
	"sync"
	"time"

	"github.com/wh-chromium/codespace-status/internal/ghcli"
	"github.com/wh-chromium/codespace-status/internal/metrics"
)

// retryDelay is how long a collector waits before reopening a failed session.
const retryDelay = 5 * time.Second

// collector samples one codespace on an interval and keeps a rolling window.
type collector struct {
	name     string
	open     Opener
	interval func() time.Duration

	mu      sync.Mutex
	samples []metrics.Sample
	lastErr string

	cancel context.CancelFunc
	done   chan struct{}
}

// newCollector builds a collector without starting it.
func newCollector(name string, open Opener, interval func() time.Duration) *collector {
	return &collector{name: name, open: open, interval: interval, done: make(chan struct{})}
}

// start launches the sampling loop.
func (c *collector) start() {
	ctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel
	go func() {
		defer close(c.done)
		c.loop(ctx)
	}()
}

// stop ends the sampling loop and waits for it to finish.
func (c *collector) stop() {
	if c.cancel != nil {
		c.cancel()
	}
	<-c.done
}

// loop keeps a session alive, reopening it after failures or when the
// sampling interval changes.
func (c *collector) loop(ctx context.Context) {
	for ctx.Err() == nil {
		interval := c.interval()
		session, err := c.open(ctx, c.name, interval)
		if err != nil {
			c.setErr(err.Error())
			if !sleep(ctx, retryDelay) {
				return
			}
			continue
		}
		reopen := c.run(ctx, session, interval)
		session.Close()
		if !reopen {
			if !sleep(ctx, retryDelay) {
				return
			}
		}
	}
}

// run reads samples from one session. It returns true when the session should
// be reopened immediately because the interval changed.
func (c *collector) run(ctx context.Context, session *ghcli.Session, interval time.Duration) bool {
	var prev metrics.Snapshot
	var havePrev bool
	for ctx.Err() == nil {
		timeout := interval + 30*time.Second
		sampleCtx, cancel := context.WithTimeout(ctx, timeout)
		snap, err := session.Sample(sampleCtx)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return false
			}
			c.setErr(err.Error())
			return false
		}
		if havePrev {
			c.add(metrics.Delta(prev, snap))
		}
		prev, havePrev = snap, true
		if c.interval() != interval {
			return true
		}
	}
	return false
}

// add appends a sample and trims the window.
func (c *collector) add(s metrics.Sample) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.samples = append(c.samples, s)
	if len(c.samples) > WindowSize {
		c.samples = c.samples[len(c.samples)-WindowSize:]
	}
	c.lastErr = ""
}

// setErr records the most recent collection failure.
func (c *collector) setErr(msg string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastErr = msg
}

// snapshot copies the current window, latest sample and error.
func (c *collector) snapshot() ([]metrics.Sample, *metrics.Sample, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]metrics.Sample, len(c.samples))
	copy(out, c.samples)
	var latest *metrics.Sample
	if len(out) > 0 {
		last := out[len(out)-1]
		latest = &last
	}
	return out, latest, c.lastErr
}

// sleep waits for d, reporting false when the context ends first.
func sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		d = time.Second
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
