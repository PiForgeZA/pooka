package pki

import (
	"crypto/x509"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEnsureCertCreatesAndReuses(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)
	c1, err := EnsureCert(dir, "192-168-68-200.sslip.io", now)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"ca.pem", "ca.key", "cert.pem", "key.pem"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
	leaf, _ := x509.ParseCertificate(c1.Certificate[0])
	caPEM, _ := os.ReadFile(CAFile(dir))
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		t.Fatal("ca.pem is not a certificate")
	}
	for _, host := range []string{"web-local.192-168-68-200.sslip.io", "api.192-168-68-200.sslip.io"} {
		if _, err := leaf.Verify(x509.VerifyOptions{DNSName: host, Roots: pool, CurrentTime: now}); err != nil {
			t.Errorf("verify %s: %v", host, err)
		}
	}
	if _, err := leaf.Verify(x509.VerifyOptions{DNSName: "a.b.192-168-68-200.sslip.io", Roots: pool, CurrentTime: now}); err == nil {
		t.Error("wildcard must cover one label only")
	}

	// Same domain, same files: the leaf is reused.
	c2, err := EnsureCert(dir, "192-168-68-200.sslip.io", now.Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if string(c2.Certificate[0]) != string(c1.Certificate[0]) {
		t.Error("leaf was reissued without need")
	}
	// A new domain gets a new leaf from the same CA.
	c3, err := EnsureCert(dir, "pooka.example.test", now)
	if err != nil {
		t.Fatal(err)
	}
	leaf3, _ := x509.ParseCertificate(c3.Certificate[0])
	if _, err := leaf3.Verify(x509.VerifyOptions{DNSName: "x-local.pooka.example.test", Roots: pool, CurrentTime: now}); err != nil {
		t.Errorf("new domain: %v", err)
	}
	// Close to expiry, the leaf is renewed.
	c4, err := EnsureCert(dir, "pooka.example.test", leaf3.NotAfter.Add(-10*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if string(c4.Certificate[0]) == string(c3.Certificate[0]) {
		t.Error("leaf not renewed near expiry")
	}
}

func TestEnsureCertNeedsDomain(t *testing.T) {
	if _, err := EnsureCert(t.TempDir(), "", time.Now()); err == nil {
		t.Error("want an error")
	}
}
