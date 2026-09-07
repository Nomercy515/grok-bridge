// Package auth provides pairing-code + bearer-token auth for LAN/Tailscale access.
//
// MVP: one shared hub token. Pair with the 6-digit code shown at startup
// (and in data/auth.json), then reuse the bearer token across desktop and phone.
// After each successful pair the pairing code is regenerated.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var AllowedMessageRoles = map[string]bool{
	"user": true, "assistant": true, "system": true,
}

// PairRateLimiter is an in-memory per-IP rate limit for pairing (default 5/60s).
type PairRateLimiter struct {
	maxAttempts int
	window      time.Duration
	mu          sync.Mutex
	hits        map[string][]time.Time
}

func NewPairRateLimiter(maxAttempts int, windowSecs float64) *PairRateLimiter {
	return &PairRateLimiter{
		maxAttempts: maxAttempts,
		window:      time.Duration(windowSecs * float64(time.Second)),
		hits:        make(map[string][]time.Time),
	}
}

func (r *PairRateLimiter) Allow(clientKey string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := clientKey
	if key == "" {
		key = "unknown"
	}
	now := time.Now()
	var kept []time.Time
	for _, t := range r.hits[key] {
		if now.Sub(t) < r.window {
			kept = append(kept, t)
		}
	}
	if len(kept) >= r.maxAttempts {
		r.hits[key] = kept
		return false
	}
	kept = append(kept, now)
	r.hits[key] = kept
	return true
}

func (r *PairRateLimiter) Reset(clientKey string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := clientKey
	if key == "" {
		key = "unknown"
	}
	delete(r.hits, key)
}

type authData struct {
	PairingCode       string   `json:"pairing_code"`
	Token             string   `json:"token"`
	CreatedAt         float64  `json:"created_at"`
	PairedAt          *float64 `json:"paired_at"`
	RotatedAt         *float64 `json:"rotated_at"`
	PairingRotatedAt  *float64 `json:"pairing_rotated_at"`
}

// Store holds pairing code + bearer token on disk (0600).
type Store struct {
	mu   sync.RWMutex
	root string
	path string
	data authData
}

func NewStore(root string) (*Store, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	s := &Store{root: root, path: filepath.Join(root, "auth.json")}
	if err := s.loadOrCreate(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) hardenMode() {
	if _, err := os.Stat(s.path); err != nil {
		return
	}
	_ = os.Chmod(s.path, 0o600)
}

func (s *Store) loadOrCreate() error {
	if b, err := os.ReadFile(s.path); err == nil {
		s.hardenMode()
		var d authData
		if json.Unmarshal(b, &d) == nil && d.Token != "" && d.PairingCode != "" {
			s.data = d
			return nil
		}
	}
	d := authData{
		PairingCode: randomPairingCode(),
		Token:       randomToken(),
		CreatedAt:   float64(time.Now().UnixNano()) / 1e9,
	}
	if err := s.write(d); err != nil {
		return err
	}
	return nil
}

func (s *Store) write(d authData) error {
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	_ = os.Chmod(tmp, 0o600)
	if err := os.Rename(tmp, s.path); err != nil {
		return err
	}
	_ = os.Chmod(s.path, 0o600)
	s.data = d
	return nil
}

func randomPairingCode() string {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		panic(err)
	}
	return fmt.Sprintf("%06d", n.Int64())
}

func randomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func (s *Store) PairingCode() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data.PairingCode
}

func (s *Store) Token() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data.Token
}

func (s *Store) Status() map[string]any {
	s.mu.RLock()
	defer s.mu.RUnlock()
	paired := s.data.PairedAt != nil
	return map[string]any{
		"needs_pairing": !paired,
		"paired":        paired,
	}
}

// Pair returns (ok, token, errorMessage). On success rotates pairing code.
func (s *Store) Pair(code string) (bool, string, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	submitted := strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	expected := s.data.PairingCode
	if submitted == "" || subtle.ConstantTimeCompare([]byte(submitted), []byte(expected)) != 1 {
		return false, "", "invalid pairing code"
	}
	token := s.data.Token
	now := float64(time.Now().UnixNano()) / 1e9
	if s.data.PairedAt == nil {
		s.data.PairedAt = &now
	}
	newCode := randomPairingCode()
	s.data.PairingCode = newCode
	s.data.PairingRotatedAt = &now
	if err := s.write(s.data); err != nil {
		return false, "", "failed to persist auth"
	}
	log.Printf("Pairing succeeded; new pairing code: %s", newCode)
	return true, token, ""
}

func (s *Store) CheckToken(token string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if token == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(token), []byte(s.data.Token)) == 1
}

func (s *Store) RotateToken() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	newToken := randomToken()
	now := float64(time.Now().UnixNano()) / 1e9
	s.data.Token = newToken
	s.data.RotatedAt = &now
	if s.data.PairedAt == nil {
		s.data.PairedAt = &now
	}
	_ = s.write(s.data)
	return newToken
}

func (s *Store) RotatePairingCode() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	newCode := randomPairingCode()
	now := float64(time.Now().UnixNano()) / 1e9
	s.data.PairingCode = newCode
	s.data.PairingRotatedAt = &now
	_ = s.write(s.data)
	log.Printf("Pairing code rotated by operator: %s", newCode)
	return newCode
}

// RotateForTests sets deterministic credentials (tests only).
func (s *Store) RotateForTests(pairingCode, token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.write(authData{
		PairingCode: pairingCode,
		Token:       token,
		CreatedAt:   float64(time.Now().UnixNano()) / 1e9,
	})
}
