package config

import (
"os"
"path/filepath"
"testing"
)

func TestLoadRejectsInvalidConfig(t *testing.T) {
	for _, body := range []string{"host: [", "template: -1", "vmid_range: [9200, 9100]"} {
		t.Run(body, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "config.yaml")
			t.Setenv("POOKA_CONFIG", p)
			if err := os.WriteFile(p, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(); err == nil {
				t.Fatal("invalid config accepted")
			}
		})
	}
}
