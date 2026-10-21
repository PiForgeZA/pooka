// Command pookad serves the Sprites-compatible API on a Proxmox host. See ADR 0004.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/PiForgeZA/pooka/internal/backend"
	"github.com/PiForgeZA/pooka/internal/backend/lxc"
	"github.com/PiForgeZA/pooka/internal/daemon"
	"github.com/PiForgeZA/pooka/internal/host"
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
}

func defaults() Config {
	return Config{
		Listen: "0.0.0.0:7700", TokenFile: "/etc/pooka/token", Org: "local",
		Template: 9000, VMIDRange: [2]int{9100, 9199}, DiskGB: 32, Cores: 4, MemoryMB: 8192,
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

	srv := &http.Server{Addr: cfg.Listen, Handler: daemon.LogRequests(log.Default(), s.Handler()), ReadHeaderTimeout: 10 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	log.Printf("pookad %s listening on %s (template %d, org %s)", Version, cfg.Listen, cfg.Template, cfg.Org)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
