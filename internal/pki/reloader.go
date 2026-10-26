package pki

import (
	"crypto/tls"
	"sync"
	"time"
)

// Reloader serves a certificate through tls.Config.GetCertificate and calls
// Load again at most once per Interval, so a renewed or replaced pair is
// served without a restart. When Load fails after a first success, the cached
// pair stays in service and the error goes to Logf.
type Reloader struct {
	Load     func(now time.Time) (tls.Certificate, error)
	Interval time.Duration        // default 24h
	Now      func() time.Time     // default time.Now
	Logf     func(string, ...any) // default: discard

	mu      sync.Mutex
	cert    *tls.Certificate
	checked time.Time
}

// GetCertificate has the signature tls.Config.GetCertificate expects. It
// ignores the client hello: the pair carries every name the proxy serves.
func (r *Reloader) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	if r.Now != nil {
		now = r.Now()
	}
	interval := r.Interval
	if interval <= 0 {
		interval = 24 * time.Hour
	}
	if r.cert != nil && now.Sub(r.checked) < interval {
		return r.cert, nil
	}
	r.checked = now
	cert, err := r.Load(now)
	if err != nil {
		if r.cert == nil {
			return nil, err
		}
		if r.Logf != nil {
			r.Logf("tls: reload failed, serving the previous certificate: %v", err)
		}
		return r.cert, nil
	}
	r.cert = &cert
	return r.cert, nil
}
