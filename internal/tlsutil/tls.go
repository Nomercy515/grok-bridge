package tlsutil

import (
	"crypto/tls"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func ResolvePaths(cert, key, envCert, envKey string) (string, string) {
	c := strings.TrimSpace(cert)
	if c == "" {
		c = strings.TrimSpace(envCert)
	}
	k := strings.TrimSpace(key)
	if k == "" {
		k = strings.TrimSpace(envKey)
	}
	return c, k
}

func Load(cert, key string) (*tls.Config, error) {
	cert = strings.TrimSpace(cert)
	key = strings.TrimSpace(key)
	if cert == "" && key == "" {
		return nil, nil
	}
	if cert == "" || key == "" {
		return nil, fmt.Errorf("both SSL cert and key are required (GROK_BRIDGE_SSL_CERT / GROK_BRIDGE_SSL_KEY or --ssl-cert / --ssl-key)")
	}
	certPath, err := filepath.Abs(cert)
	if err != nil {
		return nil, err
	}
	keyPath, err := filepath.Abs(key)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(certPath); err != nil {
		return nil, fmt.Errorf("SSL cert not found: %s", certPath)
	}
	if _, err := os.Stat(keyPath); err != nil {
		return nil, fmt.Errorf("SSL key not found: %s", keyPath)
	}
	pair, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, err
	}
	return &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12}, nil
}
