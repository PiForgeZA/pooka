// Command pookad serves the Sprites-compatible API on a Proxmox host. See ADR 0004.
// With a domain it also serves environment URLs over TLS and stops idle
// environments (ADR 0008, 0009).
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/PiForgeZA/pooka/internal/backend"
	"github.com/PiForgeZA/pooka/internal/backend/lxc"
	"github.com/PiForgeZA/pooka/internal/daemon"
	"github.com/PiForgeZA/pooka/internal/host"
	"github.com/PiForgeZA/pooka/internal/pki"
	"github.com/PiForgeZA/pooka/internal/proxy"
)

// Version is set by the linker at build time.
var Version = "dev"

// Config is /etc/pooka/pookad.yaml.
type Config struct {
	Listen    string `yaml:"listen"`
	TokenFile string `yaml:"token_file"`
	Org       string `yaml:"org"`
	Template  int    `yaml:"template"`
	VMIDRange [2]int `yaml:"vmid_range"`
	DiskGB    int    `yaml:"disk_gb"`
	Cores     int    `yaml:"cores"`
	MemoryMB  int    `yaml:"memory_mb"`
	SSHPubKey string `yaml:"ssh_pubkey"` // path on the host, optional

	// Phase 3 (SPEC section 18.7).
	Domain      string        `yaml:"domain"`       // DNS suffix of environment URLs; empty disables the proxy
	ProxyListen string        `yaml:"proxy_listen"` // TLS listener; empty disables the proxy
	TLSCert     string        `yaml:"tls_cert"`     // optional PEM pair; default is the generated one
	TLSKey      string        `yaml:"tls_key"`
	IdleTimeout time.Duration `yaml:"idle_timeout"` // 0 disables idle stop
}

func defaults() Config {
	return Config{
		Listen: "0.0.0.0:7700", TokenFile: "/etc/pooka/token", Org: "local",
		Template: 9000, VMIDRange: [2]int{9100, 9199}, DiskGB: 32, Cores: 4, MemoryMB: 8192,
		ProxyListen: "0.0.0.0:443", IdleTimeout: 30 * time.Minute,
	}
}

func load(path string) (Config, error) {
	c := defaults()
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := yaml.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("parse %s: %w", path, err)
	}
	return c, nil
}

func main() {
	cfgPath := flag.String("config", "/etc/pooka/pookad.yaml", "config file")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println("pookad version", Version)
		return
	}
	cfg, err := load(*cfgPath)
	if err != nil {
		log.Fatal(err)
	}
	tok, err := os.ReadFile(cfg.TokenFile)
	if err != nil {
		log.Fatalf("read token file %s: %v (run scripts/install-daemon.sh)", cfg.TokenFile, err)
	}
	token := strings.TrimSpace(string(tok))
	if len(token) < 16 {
		log.Fatalf("token in %s is too short; use at least 16 characters", cfg.TokenFile)
	}
	create := backend.CreateOptions{Cores: cfg.Cores, MemoryMB: cfg.MemoryMB, DiskGB: cfg.DiskGB}
	if cfg.SSHPubKey != "" {
		if k, err := os.ReadFile(cfg.SSHPubKey); err == nil {
			create.SSHPubKey = strings.TrimSpace(string(k))
		} else {
			log.Printf("warning: ssh_pubkey %s: %v", cfg.SSHPubKey, err)
		}
	}

	var h host.Host = host.Local{}
	if os.Getenv("POOKA_DEBUG") != "" {
		h = host.Logged{Host: h, Out: os.Stderr}
	}
	b := lxc.New(h, lxc.Options{Template: cfg.Template, VMIDLow: cfg.VMIDRange[0], VMIDHigh: cfg.VMIDRange[1], Version: Version})
	s := daemon.New(b, token)
	s.Org, s.Version, s.Create = cfg.Org, Version, create
	s.IdleTimeout = cfg.IdleTimeout
	apiHandler := s.Handler()
	api := daemon.LogRequests(log.Default(), apiHandler)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv := &http.Server{Addr: cfg.Listen, Handler: api, ReadHeaderTimeout: 10 * time.Second}
	servers := []*http.Server{srv}

	if cfg.Domain != "" && cfg.ProxyListen != "" {
		s.Domain = cfg.Domain
		// The pair is checked once a day, so a leaf that nears expiry, a
		// changed domain or replaced tls_cert files are served without a
		// restart. The first load runs here so a bad setup fails at start.
		tlsDir := filepath.Join(filepath.Dir(*cfgPath), "tls")
		certs := &pki.Reloader{
			Load: func(now time.Time) (tls.Certificate, error) { return loadCert(cfg, tlsDir, now) },
			Logf: log.Printf,
		}
		if _, err := certs.GetCertificate(nil); err != nil {
			log.Fatalf("proxy tls: %v", err)
		}
		// The proxy listener logs once; the API behind it must not log again.
		p := &proxy.Proxy{Backend: b, Org: cfg.Org, Domain: cfg.Domain, Token: token, API: apiHandler, Touch: s.Touch, Log: log.Default()}
		psrv := &http.Server{
			Addr:              cfg.ProxyListen,
			Handler:           daemon.LogRequests(log.Default(), p),
			TLSConfig:         &tls.Config{GetCertificate: certs.GetCertificate, MinVersion: tls.VersionTLS12},
			ReadHeaderTimeout: 10 * time.Second,
		}
		servers = append(servers, psrv)
		go func() {
			log.Printf("pookad proxy listening on %s for *.%s and api.%s", cfg.ProxyListen, cfg.Domain, cfg.Domain)
			// A proxy failure (port taken, bad certificate) must not take the
			// API on 7700 down with it.
			if err := psrv.ListenAndServeTLS("", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Printf("proxy: %v; environment URLs are unavailable, the API on %s still serves", err, cfg.Listen)
			}
		}()
	} else {
		log.Printf("proxy disabled: set domain and proxy_listen in %s to serve environment URLs", *cfgPath)
	}

	go s.RunIdle(ctx, time.Minute)
	if cfg.IdleTimeout > 0 {
		log.Printf("idle stop after %s without activity", cfg.IdleTimeout)
	}

	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for _, sv := range servers {
			_ = sv.Shutdown(shutdownCtx)
		}
	}()
	log.Printf("pookad %s listening on %s (template %d, org %s)", Version, cfg.Listen, cfg.Template, cfg.Org)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
	<-shutdownDone // let in-flight proxied requests finish too
}

// loadCert returns the configured certificate pair, or the generated one under
// dir (ADR 0008), renewed when it nears expiry or the domain changed.
func loadCert(cfg Config, dir string, now time.Time) (tls.Certificate, error) {
	if cfg.TLSCert != "" || cfg.TLSKey != "" {
		if cfg.TLSCert == "" || cfg.TLSKey == "" {
			return tls.Certificate{}, errors.New("set both tls_cert and tls_key, or neither")
		}
		return tls.LoadX509KeyPair(cfg.TLSCert, cfg.TLSKey)
	}
	cert, err := pki.EnsureCert(dir, cfg.Domain, now)
	if err != nil {
		return cert, err
	}
	log.Printf("tls: serving the generated certificate for *.%s; CA at %s", cfg.Domain, pki.CAFile(dir))
	return cert, nil
}
