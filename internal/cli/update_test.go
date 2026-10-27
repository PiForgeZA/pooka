package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PiForgeZA/pooka/internal/host"
)

// releaseServer serves a fake GitHub release: the latest-release JSON, one
// archive for this OS and architecture, and checksums.txt. checksum overrides
// the archive's real digest when it is not empty.
func releaseServer(t *testing.T, tag, binary, checksum string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "pooka", Mode: 0o755, Size: int64(len(binary))}); err != nil {
		t.Fatal(err)
	}
	tw.Write([]byte(binary))
	tw.Close()
	gz.Close()
	archive := buf.Bytes()
	name := fmt.Sprintf("pooka_%s_%s_%s.tar.gz", strings.TrimPrefix(tag, "v"), runtime.GOOS, runtime.GOARCH)
	if checksum == "" {
		sum := sha256.Sum256(archive)
		checksum = hex.EncodeToString(sum[:])
	}
	var downloads atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/PiForgeZA/pooka/releases/latest", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"tag_name": %q, "name": "pooka %s"}`, tag, tag)
	})
	mux.HandleFunc("/releases/download/"+tag+"/"+name, func(w http.ResponseWriter, _ *http.Request) {
		downloads.Add(1)
		w.Write(archive)
	})
	mux.HandleFunc("/releases/download/"+tag+"/checksums.txt", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, "%s  pooka_x_other.tar.gz\n%s  %s\n", strings.Repeat("0", 64), checksum, name)
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts, &downloads
}

func newUpdateApp(t *testing.T, fh *host.Fake, release *httptest.Server) (*App, func() string, string) {
	t.Helper()
	dir := t.TempDir()
	exe := filepath.Join(dir, "pooka")
	if err := os.WriteFile(exe, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	a, out, errb := newApp(&fakeBackend{}, "", "")
	a.Backend = nil // update and version must not need one
	a.NewHost = func(string) host.Host { return fh }
	old := daemonPoll
	daemonPoll = time.Millisecond
	t.Cleanup(func() { daemonPoll = old })
	a.Executable = func() (string, error) { return exe, nil }
	a.ReleaseAPI = release.URL + "/repos/PiForgeZA/pooka/releases/latest"
	a.ReleaseDownload = release.URL + "/releases/download"
	return a, func() string { return out.String() + errb.String() }, exe
}

func TestUpdateCLIReplacesTheBinary(t *testing.T) {
	ts, downloads := releaseServer(t, "v9.9.9", "new binary", "")
	a, out, exe := newUpdateApp(t, &host.Fake{}, ts)
	a.Cfg.Host = "" // no host configured: the daemon is skipped without --daemon
	if code := a.Execute(context.Background(), []string{"update"}); code != 0 {
		t.Fatalf("exit %d\n%s", code, out())
	}
	got, _ := os.ReadFile(exe)
	if string(got) != "new binary" {
		t.Errorf("binary not replaced: %q", got)
	}
	if st, _ := os.Stat(exe); st.Mode().Perm()&0o100 == 0 {
		t.Errorf("binary is not executable: %v", st.Mode())
	}
	if downloads.Load() != 1 {
		t.Errorf("downloads = %d, want 1", downloads.Load())
	}
	if !strings.Contains(out(), "pooka: dev -> v9.9.9") {
		t.Errorf("output:\n%s", out())
	}
	if entries, _ := os.ReadDir(filepath.Dir(exe)); len(entries) != 1 {
		t.Errorf("temp file left behind: %v", entries)
	}
}

func TestUpdateCLISkipsWhenAlreadyAtTheRelease(t *testing.T) {
	old := Version
	defer func() { Version = old }()
	Version = "9.9.9"
	ts, downloads := releaseServer(t, "v9.9.9", "new binary", "")
	a, out, exe := newUpdateApp(t, &host.Fake{}, ts)
	if code := a.Execute(context.Background(), []string{"update", "--cli"}); code != 0 {
		t.Fatalf("exit %d\n%s", code, out())
	}
	if got, _ := os.ReadFile(exe); string(got) != "old binary" {
		t.Errorf("binary replaced without need: %q", got)
	}
	if downloads.Load() != 0 || !strings.Contains(out(), "already at v9.9.9") {
		t.Errorf("downloads = %d, output:\n%s", downloads.Load(), out())
	}
}

func TestUpdateCLIPinsAVersion(t *testing.T) {
	ts, _ := releaseServer(t, "v1.2.3", "pinned", "")
	a, out, exe := newUpdateApp(t, &host.Fake{}, ts)
	if code := a.Execute(context.Background(), []string{"update", "--cli", "--version", "1.2.3"}); code != 0 {
		t.Fatalf("exit %d\n%s", code, out())
	}
	if got, _ := os.ReadFile(exe); string(got) != "pinned" {
		t.Errorf("binary: %q", got)
	}
}

func TestUpdateCLIRefusesABadChecksum(t *testing.T) {
	ts, _ := releaseServer(t, "v9.9.9", "new binary", strings.Repeat("ab", 32))
	a, out, exe := newUpdateApp(t, &host.Fake{}, ts)
	if code := a.Execute(context.Background(), []string{"update", "--cli"}); code == 0 {
		t.Fatalf("want a failure\n%s", out())
	}
	if got, _ := os.ReadFile(exe); string(got) != "old binary" {
		t.Errorf("binary replaced despite a bad checksum: %q", got)
	}
	if !strings.Contains(out(), "checksum") {
		t.Errorf("output:\n%s", out())
	}
}

func TestUpdateCLIRefusesAnUnwritableBinary(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write anywhere")
	}
	ts, _ := releaseServer(t, "v9.9.9", "new binary", "")
	a, out, exe := newUpdateApp(t, &host.Fake{}, ts)
	dir := filepath.Dir(exe)
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	if code := a.Execute(context.Background(), []string{"update", "--cli"}); code == 0 {
		t.Fatalf("want a failure\n%s", out())
	}
	if !strings.Contains(out(), dir) {
		t.Errorf("the error must name the directory:\n%s", out())
	}
}

func TestUpdateDaemonFetchesAndInstalls(t *testing.T) {
	fh := &host.Fake{}
	fh.On("uname -m", "x86_64\n")
	calls := 0
	fh.Rules = append(fh.Rules, host.FakeRule{Prefix: "/usr/local/bin/pookad --version", Fn: func(string, int) (string, int) {
		calls++
		if calls == 1 {
			return "pookad version 0.4.0\n", 0
		}
		return "pookad version 9.9.9\n", 0
	}})
	ts, downloads := releaseServer(t, "v9.9.9", "new binary", "")
	a, out, exe := newUpdateApp(t, fh, ts)
	a.Cfg.Host = "root@proxmox"
	if code := a.Execute(context.Background(), []string{"update", "--daemon"}); code != 0 {
		t.Fatalf("exit %d\n%s", code, out())
	}
	if got, _ := os.ReadFile(exe); string(got) != "old binary" || downloads.Load() != 0 {
		t.Error("--daemon must leave the CLI alone")
	}
	joined := strings.Join(fh.Calls, "\n")
	if !strings.Contains(joined, "sh -c set -e") || !strings.Contains(joined, " v9.9.9 amd64 /root/pooka-scripts") {
		t.Errorf("pookad not fetched with the tag and arch:\n%s", joined)
	}
	if !strings.Contains(joined, "bash /root/pooka-scripts/install-daemon.sh /root/pooka-scripts/pookad") {
		t.Errorf("install-daemon.sh not run:\n%s", joined)
	}
	if !strings.Contains(strings.Join(fh.Copies, "\n"), "->/root/pooka-scripts/install-daemon.sh") {
		t.Errorf("scripts not refreshed on the host: %v", fh.Copies)
	}
	if !strings.Contains(out(), "pookad: 0.4.0 -> 9.9.9") {
		t.Errorf("output:\n%s", out())
	}
}

func TestUpdateDaemonPushesALocalBinary(t *testing.T) {
	fh := &host.Fake{}
	fh.On("uname -m", "aarch64\n")
	bin := filepath.Join(t.TempDir(), "pookad")
	os.WriteFile(bin, []byte("bin"), 0o755)
	ts, _ := releaseServer(t, "v9.9.9", "new binary", "")
	a, out, _ := newUpdateApp(t, fh, ts)
	a.Cfg.Host = "root@proxmox"
	if code := a.Execute(context.Background(), []string{"update", "--daemon", "--pookad", bin}); code != 0 {
		t.Fatalf("exit %d\n%s", code, out())
	}
	if !strings.Contains(strings.Join(fh.Copies, "\n"), bin+"->/root/pooka-scripts/pookad") {
		t.Errorf("pookad not pushed: %v", fh.Copies)
	}
	for _, c := range fh.Calls {
		if strings.HasPrefix(c, "sh -c set -e") {
			t.Errorf("must not download when --pookad is given: %q", c)
		}
	}
}

func TestUpdateBothByDefaultWhenAHostIsConfigured(t *testing.T) {
	fh := &host.Fake{}
	fh.On("uname -m", "x86_64\n")
	ts, downloads := releaseServer(t, "v9.9.9", "new binary", "")
	a, out, exe := newUpdateApp(t, fh, ts)
	a.Cfg.Host = "root@proxmox"
	if code := a.Execute(context.Background(), []string{"update"}); code != 0 {
		t.Fatalf("exit %d\n%s", code, out())
	}
	if got, _ := os.ReadFile(exe); string(got) != "new binary" || downloads.Load() != 1 {
		t.Error("CLI not updated")
	}
	if !strings.Contains(strings.Join(fh.Calls, "\n"), "install-daemon.sh") {
		t.Error("daemon not updated")
	}
}

func TestVersionShowsCLIAndDaemon(t *testing.T) {
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			w.WriteHeader(404)
			return
		}
		fmt.Fprint(w, `{"status":"ok","version":"0.4.0"}`)
	}))
	defer hs.Close()
	a, out, _ := newUpdateApp(t, &host.Fake{}, hs)
	a.Cfg.DaemonURL = hs.URL
	if code := a.Execute(context.Background(), []string{"version"}); code != 0 {
		t.Fatalf("exit %d\n%s", code, out())
	}
	if !strings.Contains(out(), "pooka dev") || !strings.Contains(out(), "pookad 0.4.0") {
		t.Errorf("output:\n%s", out())
	}
}

func TestVersionOverSSH(t *testing.T) {
	fh := &host.Fake{}
	fh.On("/usr/local/bin/pookad --version", "pookad version 0.3.0\n")
	ts, _ := releaseServer(t, "v9.9.9", "x", "")
	a, out, _ := newUpdateApp(t, fh, ts)
	a.Cfg.Host = "root@proxmox"
	if code := a.Execute(context.Background(), []string{"version"}); code != 0 {
		t.Fatalf("exit %d\n%s", code, out())
	}
	if !strings.Contains(out(), "pookad 0.3.0") {
		t.Errorf("output:\n%s", out())
	}
}
