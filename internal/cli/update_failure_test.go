package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/PiForgeZA/pooka/internal/host"
)

func TestUpdateFailurePreservesExecutable(t *testing.T) {
	for _, failure := range []string{"download", "checksum download", "missing checksum", "bad checksum", "corrupt archive", "truncated archive", "missing binary", "cancel"} {
		t.Run(failure, func(t *testing.T) {
			var buf bytes.Buffer
			gz := gzip.NewWriter(&buf)
			tw := tar.NewWriter(gz)
			name := "pooka"
			if failure == "missing binary" {
				name = "other"
			}
			if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0755, Size: 3}); err != nil {
				t.Fatal(err)
			}
			tw.Write([]byte("new"))
			tw.Close()
			gz.Close()
			archive := buf.Bytes()
			if failure == "truncated archive" {
				archive = archive[:len(archive)/2]
			}
			if failure == "corrupt archive" {
				archive = []byte("not gzip")
			}
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "checksums.txt") {
					if failure == "checksum download" {
						w.WriteHeader(503)
						return
					}
					if failure == "missing checksum" {
						fmt.Fprintln(w, "no matching archive")
						return
					}
					hash := fmt.Sprintf("%x", sha256.Sum256(archive))
					if failure == "bad checksum" {
						hash = strings.Repeat("0", 64)
					}
					// The request for the archive identifies the platform without duplicating it.
					fmt.Fprintf(w, "%s  %s\n", hash, fmt.Sprintf("pooka_9.9.9_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH))
				} else {
					if failure == "download" {
						w.WriteHeader(503)
						return
					}
					w.Write(archive)
				}
			}))
			defer ts.Close()
			a, out, exe := newUpdateApp(t, &host.Fake{}, ts)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if failure == "cancel" {
				cancel()
			}
			if code := a.Execute(ctx, []string{"update", "--cli", "--version", "9.9.9"}); code == 0 {
				t.Fatalf("accepted %s: %s", failure, out())
			}
			old, err := os.ReadFile(exe)
			if err != nil || string(old) != "old binary" {
				t.Fatalf("old binary lost: %q %v", old, err)
			}
			entries, err := os.ReadDir(filepath.Dir(exe))
			if err != nil || len(entries) != 1 {
				t.Fatalf("temporary files remain: %v %v", entries, err)
			}
		})
	}
}

func TestDaemonInstallFailureIsReported(t *testing.T) {
	f := &host.Fake{}
	f.On("uname -m", "x86_64\n")
	f.On("/usr/local/bin/pookad --version", "pookad version 0.4.0\n")
	f.OnExit("bash /root/pooka-scripts/install-daemon.sh", 1, "install failed")
	ts, _ := releaseServer(t, "v9.9.9", "new", "")
	a, out, exe := newUpdateApp(t, f, ts)
	if code := a.Execute(context.Background(), []string{"update", "--version", "9.9.9"}); code == 0 {
		t.Fatal("failed install reported success")
	}
	if strings.Contains(out(), "pookad: 0.4.0 ->") {
		t.Fatalf("false success: %s", out())
	}
	old, err := os.ReadFile(exe)
	if err != nil || string(old) != "old binary" {
		t.Fatalf("CLI changed after daemon failure: %q %v", old, err)
	}
}

func TestDaemonUpdateRejectsFailedVerification(t *testing.T) {
	for _, kind := range []string{"unavailable", "wrong version"} {
		t.Run(kind, func(t *testing.T) {
			f := &host.Fake{}
			f.On("uname -m", "x86_64\n")
			f.Rules = append(f.Rules, host.FakeRule{Prefix: "/usr/local/bin/pookad --version", Fn: func(_ string, n int) (string, int) {
				if n > 1 && kind == "unavailable" {
					return "", 1
				}
				return "pookad version 0.4.0\n", 0
			}})
			ts, _ := releaseServer(t, "v9.9.9", "new", "")
			a, out, _ := newUpdateApp(t, f, ts)
			if code := a.Execute(context.Background(), []string{"update", "--daemon", "--version", "9.9.9"}); code == 0 {
				t.Fatalf("verification failure reported success: %s", out())
			}
			if strings.Contains(out(), "pookad: 0.4.0 ->") {
				t.Fatalf("false success: %s", out())
			}
		})
	}
}

func TestUpdateReplacementFailureCleansTemp(t *testing.T) {
	ts, _ := releaseServer(t, "v9.9.9", "new binary", "")
	a, out, exe := newUpdateApp(t, &host.Fake{}, ts)
	// A directory at the destination makes Rename fail after the temp file is written.
	parent := t.TempDir()
	target := filepath.Join(parent, "destination")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	a.Executable = func() (string, error) { return target, nil }
	if code := a.Execute(context.Background(), []string{"update", "--cli", "--version", "9.9.9"}); code == 0 {
		t.Fatal("replacement succeeded over a directory")
	}
	if !strings.Contains(out(), "cannot replace") {
		t.Fatalf("did not reach replacement: %s", out())
	}
	entries, err := os.ReadDir(parent)
	if err != nil || len(entries) != 1 || entries[0].Name() != "destination" {
		t.Fatalf("temp file remains: %v %v", entries, err)
	}
	old, err := os.ReadFile(exe)
	if err != nil || string(old) != "old binary" {
		t.Fatalf("old binary changed: %q %v", old, err)
	}
}
