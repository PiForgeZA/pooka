// Package config loads ~/.config/pooka/config.yaml and the per-directory .pooka file.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is SPEC section 8.
type Config struct {
	Host      string `yaml:"host"`
	Template  int    `yaml:"template"`
	Storage   string `yaml:"storage"`
	Bridge    string `yaml:"bridge"`
	VMIDRange [2]int `yaml:"vmid_range"`
	DiskGB    int    `yaml:"disk_gb"`
	Cores     int    `yaml:"cores"`
	MemoryMB  int    `yaml:"memory_mb"`
	SSHPubKey string `yaml:"ssh_pubkey"`
}

// Default returns the built-in defaults.
func Default() Config {
	return Config{
		Host:      "root@proxmox",
		Template:  9000,
		Storage:   "local-lvm",
		Bridge:    "vmbr0",
		VMIDRange: [2]int{9100, 9199},
		DiskGB:    32,
		Cores:     4,
		MemoryMB:  8192,
		SSHPubKey: "~/.ssh/id_ed25519.pub",
	}
}

// Path returns the config file path. POOKA_CONFIG overrides it.
func Path() string {
	if p := os.Getenv("POOKA_CONFIG"); p != "" {
		return p
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = filepath.Join(os.Getenv("HOME"), ".config")
	}
	return filepath.Join(dir, "pooka", "config.yaml")
}

// Load reads the config file over the defaults. A missing file is not an error.
func Load() (Config, error) {
	c := Default()
	b, err := os.ReadFile(Path())
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := yaml.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("parse %s: %w", Path(), err)
	}
	return c, c.validate()
}

func (c Config) validate() error {
	if c.Host == "" {
		return errors.New("config: host is required")
	}
	if c.Template <= 0 {
		return errors.New("config: template must be a VMID")
	}
	if c.VMIDRange[0] <= 0 || c.VMIDRange[1] < c.VMIDRange[0] {
		return errors.New("config: vmid_range must be [low, high] with low <= high")
	}
	return nil
}

// ReadSSHPubKey returns the key contents, or "" if the path is empty or missing.
func (c Config) ReadSSHPubKey() (string, error) {
	p := ExpandHome(c.SSHPubKey)
	if p == "" {
		return "", nil
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// ExpandHome replaces a leading ~ with the home directory.
func ExpandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, p[2:])
		}
	}
	return p
}

// ActiveEnvFile is the per-directory marker written by `pooka use`.
const ActiveEnvFile = ".pooka"

// ActiveEnv returns the name in ./.pooka, or "".
func ActiveEnv() string {
	b, err := os.ReadFile(ActiveEnvFile)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// SetActiveEnv writes ./.pooka, or removes it when name is empty.
func SetActiveEnv(name string) error {
	if name == "" {
		err := os.Remove(ActiveEnvFile)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	return os.WriteFile(ActiveEnvFile, []byte(name+"\n"), 0o644)
}
