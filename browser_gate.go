package main

import (
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/sirupsen/logrus"
	"github.com/xpzouying/headless_browser"
)

const defaultBrowserConcurrency = 1

var browserProcessGate struct {
	once  sync.Once
	slots chan struct{}
}

func browserConcurrencyLimit() int {
	raw := strings.TrimSpace(os.Getenv("XHS_BROWSER_CONCURRENCY"))
	if raw == "" {
		return defaultBrowserConcurrency
	}

	limit, err := strconv.Atoi(raw)
	if err != nil || limit < 1 {
		logrus.Warnf("invalid XHS_BROWSER_CONCURRENCY=%q; using %d", raw, defaultBrowserConcurrency)
		return defaultBrowserConcurrency
	}
	return limit
}

// acquireBrowserSlot keeps the number of simultaneously running Chromium
// instances bounded. On small Render instances, two concurrent browser-backed
// MCP calls can otherwise exceed the 512 MiB memory limit even when each call
// cleans up its own browser correctly.
func acquireBrowserSlot() func() {
	browserProcessGate.once.Do(func() {
		limit := browserConcurrencyLimit()
		browserProcessGate.slots = make(chan struct{}, limit)
		logrus.Infof("browser concurrency limit: %d", limit)
	})

	select {
	case browserProcessGate.slots <- struct{}{}:
	default:
		logrus.Infof("browser concurrency limit reached; waiting for an active browser to close")
		browserProcessGate.slots <- struct{}{}
	}

	var once sync.Once
	return func() {
		once.Do(func() {
			<-browserProcessGate.slots
		})
	}
}

// limitedBrowser releases its process slot exactly once when the underlying
// browser closes. Embedding preserves the existing NewPage and other browser
// methods, so callers do not need to know about the limiter.
type limitedBrowser struct {
	*headless_browser.Browser
	release   func()
	closeOnce sync.Once
}

func (b *limitedBrowser) Close() {
	if b == nil {
		return
	}

	b.closeOnce.Do(func() {
		if b.release != nil {
			defer b.release()
		}
		if b.Browser != nil {
			b.Browser.Close()
		}
	})
}
