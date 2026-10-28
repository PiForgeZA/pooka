package cli

import (
	"bytes"
	"context"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/PiForgeZA/pooka/internal/config"
)

func TestRestoreConfirmation(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		force       bool
		calls       int
	}{
		{"wrong", "other\n", false, 0}, {"EOF", "", false, 0}, {"typed", "dev1\n", false, 1}, {"force", "", true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fb := &fakeBackend{}
			a, _, _ := newApp(fb, tc.input, "")
			args := []string{"restore", "dev1", "cp-1"}
			if tc.force {
				args = append(args, "--force")
			}
			code := a.Execute(context.Background(), args)
			if (code == 0) != (tc.calls == 1) || len(fb.calls) != tc.calls {
				t.Fatalf("exit=%d calls=%v", code, fb.calls)
			}
			if tc.calls == 1 && fb.calls[0] != "restore dev1 cp-1" {
				t.Fatal(fb.calls)
			}
		})
	}
}

func TestRealClientUsesTokenAndCA(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Error("missing token")
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"sprites":[]}`))
	}))
	defer ts.Close()
	t.Setenv("POOKA_CONFIG", filepath.Join(t.TempDir(), "config.yaml"))
	for _, key := range []string{"SPRITES_API_URL", "SPRITE_TOKEN", "SPRITES_TOKEN"} {
		t.Setenv(key, "")
	}
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ts.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.DaemonURL = ts.URL
	cfg.Token = "secret"
	cfg.CAFile = ca
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
	a, err := NewApp()
	if err != nil {
		t.Fatal(err)
	}
	a.Out = &bytes.Buffer{}
	a.Err = &bytes.Buffer{}
	if code := a.Execute(context.Background(), []string{"ls"}); code != 0 {
		t.Fatalf("exit %d: %s", code, a.Err)
	}
	if err := os.WriteFile(ca, []byte("invalid CA"), 0600); err != nil {
		t.Fatal(err)
	}
	b, err := openBackend(cfg, "daemon")
	if err == nil {
		_, err = b.List(context.Background())
	}
	if err == nil {
		t.Fatal("invalid CA trusted server")
	}
	cfg.CAFile = filepath.Join(t.TempDir(), "missing")
	if _, err := openBackend(cfg, "daemon"); err == nil {
		t.Fatal("missing explicit CA accepted")
	}
}
