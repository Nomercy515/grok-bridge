// Package grokusage reports SuperGrok / Grok Build weekly pool usage.
//
// Live data comes from the same undocumented billing endpoint the Grok CLI
// /usage panel reads (GET cli-chat-proxy /v1/billing?format=credits).
// ACP x.ai/billing is pager-internal and is not called.
//
// Fallback sources, never invented numbers:
//   - ~/.grok/logs/unified.jsonl last "billing: fetched credits config"
//   - reset time parsed from a Build usage/rate-limit error
//
// signals.json is session context only and is not a weekly-quota source.
package grokusage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"grok-bridge/internal/buildsessions"
)

const (
	defaultBillingURL = "https://cli-chat-proxy.grok.com/v1/billing?format=credits"
	defaultTTL        = 90 * time.Second
	maxBody           = 1 << 20
	logScanMax        = 4 << 20 // last 4MiB of unified.jsonl
	logStaleAfter     = 36 * time.Hour
)

// Snapshot is the JSON shape returned by GET /api/usage and nested under
// GET /api/sessions as "usage". Pointer percents distinguish "unknown" from 0.
type Snapshot struct {
	Available        bool     `json:"available"`
	UsedPercent      *float64 `json:"used_percent,omitempty"`
	RemainingPercent *float64 `json:"remaining_percent,omitempty"`
	ResetAt          *string  `json:"reset_at,omitempty"` // RFC3339 UTC
	PeriodType       string   `json:"period_type,omitempty"`
	AtLimit          bool     `json:"at_limit"`
	// Five-hour / short rate window — only filled from rate-limit errors.
	// Billing credits config does not expose a 5-hour percent.
	FiveHourPercent *float64 `json:"five_hour_percent,omitempty"`
	FiveHourResetAt *string  `json:"five_hour_reset_at,omitempty"`
	FiveHourAtLimit bool     `json:"five_hour_at_limit"`
	Source           string   `json:"source"` // billing | grok-log | limit-error | none
	Reason           string   `json:"reason,omitempty"`
	FetchedAt        string   `json:"fetched_at,omitempty"`
}

// Cache holds the last known snapshot and refreshes without hammering xAI.
type Cache struct {
	mu         sync.Mutex
	snap       Snapshot
	fetched    time.Time
	ttl        time.Duration
	billingURL string
	authPath   string
	logPath    string
	httpClient *http.Client
	now        func() time.Time
}

func NewCache() *Cache {
	return &Cache{
		ttl:        defaultTTL,
		billingURL: defaultBillingURL,
		httpClient: &http.Client{Timeout: 12 * time.Second},
		now:        time.Now,
		snap: Snapshot{
			Available: false,
			Source:    "none",
			Reason:    "not fetched yet",
		},
	}
}

// Snapshot returns the last snapshot, refreshing if the TTL elapsed.
func (c *Cache) Snapshot(ctx context.Context) Snapshot {
	c.mu.Lock()
	age := c.now().Sub(c.fetched)
	need := c.fetched.IsZero() || age > c.ttl
	out := c.snap
	c.mu.Unlock()
	if !need {
		return out
	}
	return c.Refresh(ctx)
}

// Refresh fetches live billing (and fallbacks). Safe to call often; the
// mutex serializes in-flight work.
func (c *Cache) Refresh(ctx context.Context) Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if !c.fetched.IsZero() && now.Sub(c.fetched) < c.ttl {
		return c.snap
	}
	snap := c.fetchLocked(ctx, now)
	c.snap = snap
	c.fetched = now
	return snap
}

// NoteLimitError records reset time / at-limit from a Build prompt error.
// Does not invent a usage percent if billing never succeeded.
func (c *Cache) NoteLimitError(err error) {
	if err == nil {
		return
	}
	info := ParseLimitError(err.Error())
	if !info.Hit {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.snap
	if info.ResetAt != "" {
		s.ResetAt = strPtr(info.ResetAt)
	}
	if info.Exhausted {
		s.AtLimit = true
		zero := 0.0
		hundred := 100.0
		s.UsedPercent = &hundred
		s.RemainingPercent = &zero
		s.Available = true
		if s.Source == "" || s.Source == "none" {
			s.Source = "limit-error"
		}
		s.Reason = info.Reason
	} else {
		// Transient 429 / short rate window (often ~5h): do not invent a percent.
		s.FiveHourAtLimit = true
		if info.ResetAt != "" {
			s.FiveHourResetAt = strPtr(info.ResetAt)
		}
		if s.UsedPercent == nil {
			s.Source = "limit-error"
			s.Reason = info.Reason
			s.Available = s.ResetAt != nil || s.FiveHourResetAt != nil
		} else if s.Reason == "" {
			s.Reason = info.Reason
		}
	}
	s.FetchedAt = c.now().UTC().Format(time.RFC3339)
	c.snap = s
}

func (c *Cache) fetchLocked(ctx context.Context, now time.Time) Snapshot {
	home := buildsessions.GrokHome()
	authPath := c.authPath
	if authPath == "" && home != "" {
		authPath = filepath.Join(home, "auth.json")
	}
	logPath := c.logPath
	if logPath == "" && home != "" {
		logPath = filepath.Join(home, "logs", "unified.jsonl")
	}

	prevFive := c.snap
	if raw, err := fetchBilling(ctx, c.httpClient, c.billingURL, authPath); err == nil {
		s, perr := parseBilling(raw, now)
		if perr == nil && s.Available {
			s.Source = "billing"
			s.FetchedAt = now.UTC().Format(time.RFC3339)
			// Keep short-window rate-limit signal; billing has no 5h percent.
			s.FiveHourPercent = prevFive.FiveHourPercent
			s.FiveHourResetAt = prevFive.FiveHourResetAt
			s.FiveHourAtLimit = prevFive.FiveHourAtLimit
			return s
		}
		if perr != nil {
			// fall through to log
			_ = perr
		} else if s.Reason != "" {
			// parsed but unavailable — still try log, keep reason if log fails
		}
	}

	if raw, ts, err := readUnifiedBilling(logPath); err == nil {
		s, perr := parseBilling(raw, now)
		if perr == nil && s.Available {
			s.Source = "grok-log"
			if ts.IsZero() {
				s.FetchedAt = now.UTC().Format(time.RFC3339)
			} else {
				s.FetchedAt = ts.UTC().Format(time.RFC3339)
				if now.Sub(ts) > logStaleAfter {
					s.Reason = "from grok log; may be stale"
				}
			}
			return s
		}
	}

	// Preserve a previous limit-error reset rather than wiping it.
	prev := c.snap
	if prev.Source == "limit-error" && (prev.ResetAt != nil || prev.AtLimit) {
		prev.FetchedAt = now.UTC().Format(time.RFC3339)
		return prev
	}

	return Snapshot{
		Available: false,
		Source:    "none",
		Reason:    "usage unknown (no billing token/response)",
		FetchedAt: now.UTC().Format(time.RFC3339),
		ResetAt:   prev.ResetAt,
		AtLimit:   prev.AtLimit,
	}
}

func fetchBilling(ctx context.Context, client *http.Client, url, authPath string) ([]byte, error) {
	tok, err := loadGrokToken(authPath)
	if err != nil {
		return nil, err
	}
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("X-XAI-Token-Auth", "xai-grok-cli")
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("billing HTTP %d", resp.StatusCode)
	}
	return body, nil
}

func loadGrokToken(path string) (string, error) {
	if path == "" {
		return "", errors.New("no grok auth path")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return "", err
	}
	if v, ok := top["access_token"]; ok {
		var s string
		if json.Unmarshal(v, &s) == nil && s != "" {
			return s, nil
		}
	}
	var best string
	for k, v := range top {
		var e struct {
			Key string `json:"key"`
		}
		if json.Unmarshal(v, &e) != nil || e.Key == "" {
			continue
		}
		if strings.Contains(strings.ToLower(k), "auth.x.ai") {
			return e.Key, nil
		}
		if len(e.Key) > len(best) {
			best = e.Key
		}
	}
	if best != "" {
		return best, nil
	}
	return "", errors.New("no access token in grok auth.json")
}

func strPtr(s string) *string { return &s }

func floatPtr(f float64) *float64 { return &f }
