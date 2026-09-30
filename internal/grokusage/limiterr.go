package grokusage

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ACP JSON-RPC code grok-build uses for HTTP 429 / rate-limit.
const acpRateLimitedCode = -32003

var (
	rfc3339Re = regexp.MustCompile(`20\d{2}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})`)
	retryRe   = regexp.MustCompile(`(?i)retry[_-]?after["\s:=]+(\d+)`)
)

// LimitInfo is extracted from a Build/ACP error payload. Percent is never
// guessed here unless the error itself is clearly weekly-pool exhaustion.
type LimitInfo struct {
	Hit       bool
	Exhausted bool   // weekly / usage pool empty (not a transient 429)
	ResetAt   string // RFC3339 UTC, optional
	Reason    string
}

func ParseLimitError(text string) LimitInfo {
	if strings.TrimSpace(text) == "" {
		return LimitInfo{}
	}
	low := strings.ToLower(text)
	exhausted := strings.Contains(low, "subscription:free-usage-exhausted") ||
		strings.Contains(low, "weekly") && strings.Contains(low, "limit") && (strings.Contains(low, "exceed") || strings.Contains(low, "reached") || strings.Contains(low, "exhausted")) ||
		strings.Contains(low, "usage limit") ||
		strings.Contains(low, "you've reached your") && strings.Contains(low, "usage") ||
		strings.Contains(low, "you’ve reached your") && strings.Contains(low, "usage")

	rateLimited := strings.Contains(low, "rate limited") ||
		strings.Contains(low, `"code":-32003`) ||
		strings.Contains(text, strconv.Itoa(acpRateLimitedCode))

	if !exhausted && !rateLimited {
		return LimitInfo{}
	}

	info := LimitInfo{Hit: exhausted || rateLimited, Exhausted: exhausted}
	if exhausted {
		info.Reason = "build usage limit"
	} else {
		info.Reason = "rate limited"
	}

	if m := rfc3339Re.FindString(text); m != "" {
		if t, err := parseTime(m); err == nil {
			info.ResetAt = t.UTC().Format(time.RFC3339)
		}
	}
	if info.ResetAt == "" {
		if m := retryRe.FindStringSubmatch(text); len(m) == 2 {
			sec, _ := strconv.Atoi(m[1])
			if sec > 0 && sec < 14*24*3600 {
				info.ResetAt = time.Now().UTC().Add(time.Duration(sec) * time.Second).Format(time.RFC3339)
			}
		}
	}
	return info
}
