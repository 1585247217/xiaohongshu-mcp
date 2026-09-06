package main

import "testing"

func TestBrowserConcurrencyLimit(t *testing.T) {
	t.Setenv("XHS_BROWSER_CONCURRENCY", "")
	if got := browserConcurrencyLimit(); got != 1 {
		t.Fatalf("empty setting: got %d, want 1", got)
	}

	t.Setenv("XHS_BROWSER_CONCURRENCY", "2")
	if got := browserConcurrencyLimit(); got != 2 {
		t.Fatalf("explicit setting: got %d, want 2", got)
	}

	t.Setenv("XHS_BROWSER_CONCURRENCY", "0")
	if got := browserConcurrencyLimit(); got != 1 {
		t.Fatalf("zero setting: got %d, want fallback 1", got)
	}

	t.Setenv("XHS_BROWSER_CONCURRENCY", "invalid")
	if got := browserConcurrencyLimit(); got != 1 {
		t.Fatalf("invalid setting: got %d, want fallback 1", got)
	}
}
