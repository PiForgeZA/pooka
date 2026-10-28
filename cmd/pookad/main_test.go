package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/PiForgeZA/pooka/internal/backend/backendtest"
	"github.com/PiForgeZA/pooka/internal/daemon"
	"github.com/PiForgeZA/pooka/internal/pki"
	"github.com/PiForgeZA/pooka/internal/proxy"
)

func TestConfigAndCertificateErrors(t *testing.T) {
	p := filepath.Join(t.TempDir(), "pookad.yaml")
	c, err := load(p)
	if err != nil || c.Listen == "" || c.IdleTimeout != 30*time.Minute {
		t.Fatalf("defaults=%+v error=%v", c, err)
	}
	if err := os.WriteFile(p, []byte("listen: ["), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := load(p); err == nil {
		t.Fatal("invalid config accepted")
	}
	for _, c := range []Config{{TLSCert: "only-cert"}, {TLSKey: "only-key"}, {TLSCert: "missing-cert", TLSKey: "missing-key"}} {
		if _, err := loadCert(c, t.TempDir(), time.Now()); err == nil {
			t.Fatal("invalid pair accepted")
		}
	}
}

// Exercise the daemon, proxy and certificate loader together without Proxmox.
// main's process signals and service installation remain integration concerns.
func TestTLSAPIStartupAndShutdown(t *testing.T) {
	dir := t.TempDir()
	cfg := defaults()
	cfg.Domain = "test.local"
	cert, err := loadCert(cfg, dir, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	ca, err := os.ReadFile(pki.CAFile(dir))
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		t.Fatal("invalid CA")
	}
	fb := backendtest.New()
	s := daemon.New(fb, "secret")
	p := &proxy.Proxy{Backend: fb, Org: cfg.Org, Domain: cfg.Domain, Token: "secret", API: s.Handler(), Touch: s.Touch}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: p, TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}}
	done := make(chan error, 1)
	go func() { done <- server.ServeTLS(listener, "", "") }()
	defer server.Close()
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: "api.test.local", MinVersion: tls.VersionTLS12}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: time.Second}
	req, err := http.NewRequest("GET", "https://"+listener.Addr().String()+"/v1/sprites", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "api.test.local"
	req.Header.Set("Authorization", "Bearer secret")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatal(resp.Status)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != http.ErrServerClosed {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("server did not stop")
	}
}
