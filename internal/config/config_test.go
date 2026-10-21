package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadDaemonSettingsAndEnv(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	tokPath := filepath.Join(dir, "token")
	os.WriteFile(cfgPath, []byte("daemon_url: http://proxmox:7700\ntoken_file: "+tokPath+"\n"), 0o600)
	os.WriteFile(tokPath, []byte("  file-token \n"), 0o600)
	t.Setenv("POOKA_CONFIG", cfgPath)
	t.Setenv("SPRITES_API_URL", "")
	t.Setenv("SPRITE_TOKEN", "")
	t.Setenv("SPRITE_API_TOKEN", "")

	c, err := Load()
	if err != nil || c.DaemonURL != "http://proxmox:7700" || c.Host != "root@proxmox" {
		t.Fatalf("load: %v %+v", err, c)
	}
	if tok, err := c.DaemonToken(); err != nil || tok != "file-token" {
		t.Errorf("token from file: %q %v", tok, err)
	}

	t.Setenv("SPRITES_API_URL", "http://other:7700")
	t.Setenv("SPRITE_TOKEN", "env-token")
	c, _ = Load()
	if c.DaemonURL != "http://other:7700" {
		t.Errorf("env url: %s", c.DaemonURL)
	}
	if tok, _ := c.DaemonToken(); tok != "env-token" {
		t.Errorf("env token: %s", tok)
	}

	c = Default()
	c.DaemonURL = "http://x"
	if _, err := c.DaemonToken(); err == nil || !strings.Contains(err.Error(), "SPRITE_TOKEN") {
		t.Errorf("missing token must name the fix: %v", err)
	}
}
