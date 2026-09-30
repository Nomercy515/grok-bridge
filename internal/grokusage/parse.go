package grokusage

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

type billingEnvelope struct {
	Config *billingConfig `json:"config"`
}

type billingConfig struct {
	CreditUsagePercent *float64     `json:"creditUsagePercent"`
	CurrentPeriod      *usagePeriod `json:"currentPeriod"`
	BillingPeriodEnd   string       `json:"billingPeriodEnd"`
	ProductUsage       []struct {
		Product      string   `json:"product"`
		UsagePercent *float64 `json:"usagePercent"`
	} `json:"productUsage"`
}

type usagePeriod struct {
	Type  string `json:"type"`
	Start string `json:"start"`
	End   string `json:"end"`
}

func parseBilling(raw []byte, now time.Time) (Snapshot, error) {
	var env billingEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return Snapshot{Available: false, Source: "none", Reason: "unparseable billing json"}, err
	}
	if env.Config == nil {
		return Snapshot{Available: false, Source: "none", Reason: "billing response has no config"}, fmt.Errorf("no config")
	}
	cfg := env.Config
	used := cfg.CreditUsagePercent
	// Prefer the shared weekly tank. Build's product row is a breakdown of
	// the same pool — do not substitute it for the tank unless the tank
	// percent is absent.
	if used == nil {
		for _, p := range cfg.ProductUsage {
			if p.UsagePercent == nil {
				continue
			}
			n := strings.ToLower(strings.ReplaceAll(p.Product, "_", ""))
			n = strings.ReplaceAll(n, "-", "")
			if strings.Contains(n, "grokbuild") {
				used = p.UsagePercent
				break
			}
		}
	}
	if used == nil {
		return Snapshot{Available: false, Source: "none", Reason: "no creditUsagePercent"}, fmt.Errorf("no percent")
	}
	if *used < 0 || *used > 100 {
		return Snapshot{Available: false, Source: "none", Reason: fmt.Sprintf("creditUsagePercent %.2f out of range", *used)}, fmt.Errorf("out of range")
	}
	rem := 100 - *used
	s := Snapshot{
		Available:        true,
		UsedPercent:      floatPtr(*used),
		RemainingPercent: floatPtr(rem),
		AtLimit:          rem <= 0.05,
	}
	end := ""
	if cfg.CurrentPeriod != nil {
		end = strings.TrimSpace(cfg.CurrentPeriod.End)
		s.PeriodType = strings.TrimSpace(cfg.CurrentPeriod.Type)
	}
	if end == "" {
		end = strings.TrimSpace(cfg.BillingPeriodEnd)
	}
	if end != "" {
		if t, err := parseTime(end); err == nil {
			rfc := t.UTC().Format(time.RFC3339)
			s.ResetAt = &rfc
		}
	}
	if s.PeriodType == "" {
		s.PeriodType = "USAGE_PERIOD_TYPE_WEEKLY"
	}
	_ = now
	return s, nil
}

func parseTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05.999999999Z07:00",
		"2006-01-02T15:04:05Z07:00",
	}
	var last error
	for _, l := range layouts {
		t, err := time.Parse(l, s)
		if err == nil {
			return t, nil
		}
		last = err
	}
	return time.Time{}, last
}

type unifiedLine struct {
	Msg string          `json:"msg"`
	TS  string          `json:"ts"`
	Ctx json.RawMessage `json:"ctx"`
}

func readUnifiedBilling(path string) ([]byte, time.Time, error) {
	if path == "" {
		return nil, time.Time{}, fmt.Errorf("no log path")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, time.Time{}, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, time.Time{}, err
	}
	start := st.Size() - logScanMax
	if start < 0 {
		start = 0
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil, time.Time{}, err
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var last []byte
	var lastTS time.Time
	first := start > 0
	for sc.Scan() {
		line := sc.Bytes()
		if first {
			first = false
			continue // likely a partial line after seek
		}
		if !bytes.Contains(line, []byte("billing: fetched credits config")) {
			continue
		}
		var rec unifiedLine
		if json.Unmarshal(line, &rec) != nil {
			continue
		}
		if rec.Msg != "billing: fetched credits config" {
			continue
		}
		cfg := extractCtxConfig(rec.Ctx)
		if cfg == nil {
			continue
		}
		last = cfg
		if t, err := parseTime(rec.TS); err == nil {
			lastTS = t
		}
	}
	if last == nil {
		return nil, time.Time{}, fmt.Errorf("no billing log line")
	}
	return last, lastTS, nil
}

func extractCtxConfig(ctx json.RawMessage) []byte {
	if len(ctx) == 0 {
		return nil
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(ctx, &obj) != nil {
		return nil
	}
	cfg, ok := obj["config"]
	if !ok || len(cfg) == 0 || string(cfg) == "null" {
		return nil
	}
	env, err := json.Marshal(map[string]json.RawMessage{"config": cfg})
	if err != nil {
		return nil
	}
	return env
}
