package grokusage

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseBillingWeeklyTank(t *testing.T) {
	raw := []byte(`{"config":{"currentPeriod":{"type":"USAGE_PERIOD_TYPE_WEEKLY","start":"2026-08-08T01:53:09.930537+00:00","end":"2026-08-15T01:53:09.930537+00:00"},"creditUsagePercent":100.0,"productUsage":[{"product":"GrokBuild","usagePercent":100.0}],"isUnifiedBillingUser":true,"billingPeriodEnd":"2026-08-15T01:53:09.930537+00:00"}}`)
	s, err := parseBilling(raw, time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if !s.Available || s.UsedPercent == nil || *s.UsedPercent != 100 {
		t.Fatalf("used=%v avail=%v", s.UsedPercent, s.Available)
	}
	if s.RemainingPercent == nil || *s.RemainingPercent != 0 {
		t.Fatalf("remaining=%v", s.RemainingPercent)
	}
	if !s.AtLimit {
		t.Fatal("expected at limit")
	}
	if s.ResetAt == nil || !strings.HasPrefix(*s.ResetAt, "2026-08-15T01:53:09") {
		t.Fatalf("reset=%v", s.ResetAt)
	}
}

func TestParseBillingMissingPercentUnavailable(t *testing.T) {
	raw := []byte(`{"config":{"currentPeriod":{"end":"2026-08-15T00:00:00Z"}}}`)
	s, err := parseBilling(raw, time.Now())
	if err == nil || s.Available {
		t.Fatalf("expected unavailable, got %+v err=%v", s, err)
	}
}

func TestParseBillingOutOfRangeUnavailable(t *testing.T) {
	raw := []byte(`{"config":{"creditUsagePercent":140}}`)
	s, err := parseBilling(raw, time.Now())
	if err == nil || s.Available {
		t.Fatalf("expected unavailable, got %+v", s)
	}
}

func TestParseBillingDoesNotInventFromEmpty(t *testing.T) {
	s, err := parseBilling([]byte(`{}`), time.Now())
	if err == nil || s.Available || s.UsedPercent != nil {
		t.Fatalf("invented numbers: %+v err=%v", s, err)
	}
}

func TestLoadGrokTokenNestedAuthXAI(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "auth.json")
	key := strings.Repeat("k", 220)
	doc := map[string]any{
		"https://auth.x.ai::user": map[string]any{"key": key},
	}
	b, _ := json.Marshal(doc)
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := loadGrokToken(p)
	if err != nil || got != key {
		t.Fatalf("got %q err=%v", got, err)
	}
}

func TestCacheBillingHTTP(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok-live" {
			http.Error(w, "nope", 401)
			return
		}
		if r.Header.Get("X-XAI-Token-Auth") != "xai-grok-cli" {
			http.Error(w, "hdr", 400)
			return
		}
		_, _ = w.Write([]byte(`{"config":{"creditUsagePercent":42.5,"currentPeriod":{"type":"USAGE_PERIOD_TYPE_WEEKLY","end":"2026-09-20T12:00:00Z"}}}`))
	}))
	defer ts.Close()

	dir := t.TempDir()
	auth := filepath.Join(dir, "auth.json")
	_ = os.WriteFile(auth, []byte(`{"access_token":"tok-live"}`), 0o600)

	c := NewCache()
	c.billingURL = ts.URL
	c.authPath = auth
	c.logPath = filepath.Join(dir, "missing.jsonl")
	c.ttl = time.Minute

	s := c.Refresh(context.Background())
	if !s.Available || s.Source != "billing" || s.UsedPercent == nil || *s.UsedPercent != 42.5 {
		t.Fatalf("snap %+v", s)
	}
	if s.RemainingPercent == nil || *s.RemainingPercent != 57.5 {
		t.Fatalf("remaining %+v", s)
	}
	if s.ResetAt == nil || *s.ResetAt != "2026-09-20T12:00:00Z" {
		t.Fatalf("reset %v", s.ResetAt)
	}
}

func TestCacheFallsBackToUnifiedLog(t *testing.T) {
	dir := t.TempDir()
	logp := filepath.Join(dir, "unified.jsonl")
	line := `{"msg":"billing: fetched credits config","ts":"2026-09-15T10:00:00Z","ctx":{"config":{"creditUsagePercent":87.0,"currentPeriod":{"end":"2026-09-18T15:30:00Z"}}}}` + "\n"
	if err := os.WriteFile(logp, []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	c := NewCache()
	c.authPath = filepath.Join(dir, "no-auth.json")
	c.logPath = logp
	s := c.Refresh(context.Background())
	if !s.Available || s.Source != "grok-log" {
		t.Fatalf("snap %+v", s)
	}
	if s.UsedPercent == nil || *s.UsedPercent != 87 {
		t.Fatalf("used %v", s.UsedPercent)
	}
}

func TestCacheUnknownWhenNoSources(t *testing.T) {
	dir := t.TempDir()
	c := NewCache()
	c.authPath = filepath.Join(dir, "nope.json")
	c.logPath = filepath.Join(dir, "nope.jsonl")
	s := c.Refresh(context.Background())
	if s.Available || s.UsedPercent != nil {
		t.Fatalf("invented %+v", s)
	}
	if s.Source != "none" {
		t.Fatalf("source %s", s.Source)
	}
}

func TestNoteLimitErrorSetsResetWithoutInventingWhenAlreadyKnown(t *testing.T) {
	c := NewCache()
	used := 40.0
	rem := 60.0
	c.snap = Snapshot{Available: true, UsedPercent: &used, RemainingPercent: &rem, Source: "billing"}
	c.NoteLimitError(errors.New(`session/prompt: Rate limited ({"code":-32003,"message":"Rate limited","data":"retry later 2026-09-22T11:00:00Z"})`))
	s := c.snap
	if s.ResetAt == nil || *s.ResetAt != "2026-09-22T11:00:00Z" {
		t.Fatalf("reset %v", s.ResetAt)
	}
	if s.UsedPercent == nil || *s.UsedPercent != 40 {
		t.Fatalf("transient 429 must not invent 100%%, got %v", s.UsedPercent)
	}
}

func TestNoteLimitErrorExhaustionForcesZeroRemaining(t *testing.T) {
	c := NewCache()
	used := 40.0
	rem := 60.0
	c.snap = Snapshot{Available: true, UsedPercent: &used, RemainingPercent: &rem, Source: "billing"}
	c.NoteLimitError(errors.New("subscription:free-usage-exhausted weekly limit reached; resets 2026-09-22T11:00:00Z"))
	s := c.snap
	if !s.AtLimit {
		t.Fatal("expected at limit")
	}
	if s.UsedPercent == nil || *s.UsedPercent != 100 {
		t.Fatalf("after exhaustion used should be 100, got %v", s.UsedPercent)
	}
	if s.ResetAt == nil || *s.ResetAt != "2026-09-22T11:00:00Z" {
		t.Fatalf("reset %v", s.ResetAt)
	}
}

func TestNoteLimitErrorUsageUnknownStillShowsReset(t *testing.T) {
	c := NewCache()
	c.NoteLimitError(errors.New("You've reached your free Grok Build usage limit for now. retry_after=3600"))
	s := c.snap
	if !s.AtLimit || !s.Available {
		t.Fatalf("snap %+v", s)
	}
	if s.Source != "limit-error" {
		t.Fatalf("source %s", s.Source)
	}
	if s.RemainingPercent == nil || *s.RemainingPercent != 0 {
		t.Fatalf("remaining %v", s.RemainingPercent)
	}
}

func TestParseLimitErrorIgnoresUnrelated(t *testing.T) {
	if ParseLimitError("connection reset by peer").Hit {
		t.Fatal("false positive")
	}
}
