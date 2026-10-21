package httpapi

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PiForgeZA/pooka/internal/backend"
	"github.com/PiForgeZA/pooka/internal/backend/backendtest"
	"github.com/PiForgeZA/pooka/internal/daemon"
)

// newClient runs the real daemon on the in-memory backend and returns a client for it.
func newClient(t *testing.T) (*Client, *backendtest.Fake) {
	t.Helper()
	fb := backendtest.New()
	s := daemon.New(fb, "secret")
	s.Create = backend.CreateOptions{Cores: 4, MemoryMB: 8192, DiskGB: 32, SSHPubKey: "daemon-key"}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return New(ts.URL, "secret"), fb
}

func TestListGetAndErrors(t *testing.T) {
	c, _ := newClient(t)
	ctx := context.Background()
	envs, err := c.List(ctx)
	if err != nil || len(envs) != 2 {
		t.Fatalf("list: %v %+v", err, envs)
	}
	if envs[0].Name != "dev1" || envs[0].VMID != 9100 || envs[0].Status != "running" || envs[0].IP != "10.0.0.5" || envs[0].Created.Year() != 2026 {
		t.Errorf("dev1: %+v", envs[0])
	}
	if envs[1].Status != "stopped" || envs[1].IP != "" || envs[1].VMID != 9101 {
		t.Errorf("dev2: %+v", envs[1])
	}
	if e, err := c.Get(ctx, "dev2"); err != nil || e.Name != "dev2" {
		t.Errorf("get: %v %+v", err, e)
	}
	_, err = c.Get(ctx, "nope")
	if !errors.Is(err, backend.ErrNotFound) || err.Error() != "environment not found" {
		t.Errorf("missing: %v", err)
	}

	bad := New(c.URL, "wrong")
	if _, err := bad.List(ctx); err == nil || !strings.Contains(err.Error(), "SPRITE_TOKEN") {
		t.Errorf("wrong token: %v", err)
	}
	old := httptest.NewServer(http.NotFoundHandler()) // a pookad without the route
	t.Cleanup(old.Close)
	if err := New(old.URL, "x").Start(ctx, "dev1"); !errors.Is(err, backend.ErrNotFound) || !strings.Contains(err.Error(), "upgrade it") {
		t.Errorf("old daemon: %v", err)
	}
	down := New("http://127.0.0.1:1", "x")
	if _, err := down.List(ctx); err == nil || !strings.Contains(err.Error(), "cannot reach pookad at http://127.0.0.1:1") {
		t.Errorf("unreachable: %v", err)
	}
}

func TestLifecycle(t *testing.T) {
	c, fb := newClient(t)
	ctx := context.Background()
	e, err := c.Create(ctx, "dev3", backend.CreateOptions{Cores: 2, SSHPubKey: "client-key"})
	if err != nil || e.Name != "dev3" || e.VMID != 9102 || e.IP != "10.0.0.7" {
		t.Fatalf("create: %v %+v", err, e)
	}
	if fb.Created.Cores != 2 || fb.Created.MemoryMB != 8192 || fb.Created.SSHPubKey != "client-key" {
		t.Errorf("options: %+v", fb.Created)
	}
	if _, err := c.Create(ctx, "dev3", backend.CreateOptions{}); !errors.Is(err, backend.ErrExists) || strings.Count(err.Error(), "already exists") != 1 {
		t.Errorf("duplicate: %v", err)
	}
	if _, err := c.Create(ctx, "Bad Name", backend.CreateOptions{}); err == nil || !strings.Contains(err.Error(), "invalid name") {
		t.Errorf("bad name must fail before the request: %v", err)
	}
	if err := c.Start(ctx, "dev2"); err != nil {
		t.Errorf("start: %v", err)
	}
	if err := c.Stop(ctx, "dev1"); err != nil {
		t.Errorf("stop: %v", err)
	}
	if err := c.Start(ctx, "nope"); !errors.Is(err, backend.ErrNotFound) {
		t.Errorf("start missing: %v", err)
	}
	if err := c.Destroy(ctx, "dev3"); err != nil {
		t.Errorf("destroy: %v", err)
	}
	want := "create dev3,start dev2,stop dev1,destroy dev3"
	if got := strings.Join(fb.Calls, ","); got != want {
		t.Errorf("calls %q, want %q", got, want)
	}
}

func TestCheckpoints(t *testing.T) {
	c, fb := newClient(t)
	var progress bytes.Buffer
	c.Progress = &progress
	ctx := context.Background()
	cp, err := c.Checkpoint(ctx, "dev1", "hello")
	if err != nil || cp.ID != "cp-2" || cp.Comment != "hello" || cp.Time.IsZero() {
		t.Fatalf("checkpoint: %v %+v", err, cp)
	}
	if !strings.Contains(progress.String(), "Creating checkpoint") {
		t.Errorf("progress: %q", progress.String())
	}
	cps, err := c.ListCheckpoints(ctx, "dev1")
	if err != nil || len(cps) != 2 || cps[0].ID != "cp-1" || cps[1].ID != "cp-2" {
		t.Errorf("list must be oldest first: %v %+v", err, cps)
	}
	// An older checkpoint listed after the newest must not win: pick by time.
	fb.Cps["dev1"] = append(fb.Cps["dev1"], backend.Checkpoint{ID: "cp-old", Time: time.Unix(1600000000, 0)})
	if cp, err := c.Checkpoint(ctx, "dev1", "third"); err != nil || cp.ID != "cp-4" {
		t.Errorf("newest by time: %v %+v", err, cp)
	}
	if err := c.DeleteCheckpoint(ctx, "dev1", "cp-1"); err != nil {
		t.Errorf("delete: %v", err)
	}
	if err := c.DeleteCheckpoint(ctx, "dev1", "cp-1"); !errors.Is(err, backend.ErrNotFound) {
		t.Errorf("delete missing: %v", err)
	}
	if err := c.Restore(ctx, "dev1", "cp-2"); err != nil {
		t.Errorf("restore: %v", err)
	}
	if last := fb.Calls[len(fb.Calls)-1]; last != "restore dev1 cp-2" {
		t.Errorf("calls: %v", fb.Calls)
	}
	if _, err := c.Checkpoint(ctx, "nope", ""); !errors.Is(err, backend.ErrNotFound) {
		t.Errorf("checkpoint missing env: %v", err)
	}
}

func TestExec(t *testing.T) {
	c, fb := newClient(t)
	ctx := context.Background()
	var out, errb bytes.Buffer
	code, err := c.Exec(ctx, "dev1", []string{"echo", "hi"}, backend.ExecOptions{Stdin: strings.NewReader("hello"), Stdout: &out, Stderr: &errb, Env: []string{"A=1"}, Dir: "/tmp"})
	if err != nil || code != 0 {
		t.Fatalf("exec: %v %d", err, code)
	}
	for _, want := range []string{"stdin:hello", "cmd:echo hi", "env:A=1", "dir:/tmp"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("stdout lacks %q: %q", want, out.String())
		}
	}
	if errb.String() != "err\n" {
		t.Errorf("stderr: %q", errb.String())
	}
	if code, err := c.Exec(ctx, "dev1", []string{"exit"}, backend.ExecOptions{}); err != nil || code != 7 {
		t.Errorf("exit code: %v %d", err, code)
	}
	if _, err := c.Exec(ctx, "dev1", []string{"ls"}, backend.ExecOptions{Root: true}); err != nil {
		t.Errorf("root: %v", err)
	}
	if last := fb.Calls[len(fb.Calls)-1]; last != "ls" || !fb.Root[len(fb.Root)-1] {
		t.Errorf("root must go through the extension route as root: %q root=%v", last, fb.Root)
	}
	if _, err := c.Exec(ctx, "nope", []string{"ls"}, backend.ExecOptions{}); !errors.Is(err, backend.ErrNotFound) {
		t.Errorf("missing env: %v", err)
	}
}

// fakeTerm stands in for the local terminal.
type fakeTerm struct {
	stdin   io.Reader
	stdout  bytes.Buffer
	raw     bool
	restore bool
	resize  chan struct{}
}

func (f *fakeTerm) in() io.Reader            { return f.stdin }
func (f *fakeTerm) out() io.Writer           { return &f.stdout }
func (f *fakeTerm) size() (uint16, uint16)   { return 30, 100 }
func (f *fakeTerm) makeRaw() (func(), error) { f.raw = true; return func() { f.restore = true }, nil }
func (f *fakeTerm) resizes(context.Context) <-chan struct{} {
	return f.resize
}

func TestExecTTY(t *testing.T) {
	c, _ := newClient(t)
	ft := &fakeTerm{stdin: strings.NewReader("typed"), resize: make(chan struct{}, 1)}
	ft.resize <- struct{}{}
	c.term = ft
	t.Setenv("TERM", "xterm-test")
	code, err := c.Exec(context.Background(), "dev1", []string{"bash", "-l"}, backend.ExecOptions{TTY: true})
	if err != nil || code != 0 {
		t.Fatalf("exec: %v %d", err, code)
	}
	if !ft.raw || !ft.restore {
		t.Errorf("raw mode not set and restored: raw=%v restore=%v", ft.raw, ft.restore)
	}
	out := ft.stdout.String()
	if !strings.Contains(out, "stdin:typed") || !strings.Contains(out, "cmd:bash -l") || !strings.Contains(out, "env:TERM=xterm-test") {
		t.Errorf("terminal output: %q", out)
	}
}

func TestPushPull(t *testing.T) {
	c, fb := newClient(t)
	ctx := context.Background()
	dir := t.TempDir()
	local := filepath.Join(dir, "notes.md")
	os.WriteFile(local, []byte("# hi\n"), 0o640)
	if err := c.Push(ctx, "dev1", local, "/home/pooka/notes.md"); err != nil {
		t.Fatalf("push: %v", err)
	}
	if fb.Pushed["/home/pooka/notes.md"] != "# hi\n" {
		t.Errorf("pushed: %q", fb.Pushed)
	}
	n := len(fb.Calls)
	if !strings.Contains(fb.Calls[n-2], "/home/pooka/notes.md 0640") || fb.Calls[n-1] != "chown pooka:pooka /home/pooka/notes.md" {
		t.Errorf("calls: %v", fb.Calls[n-2:])
	}
	back := filepath.Join(dir, "back.md")
	if err := c.Pull(ctx, "dev1", "/home/pooka/notes.md", back); err != nil {
		t.Fatalf("pull: %v", err)
	}
	if b, _ := os.ReadFile(back); string(b) != "# hi\n" {
		t.Errorf("pulled: %q", b)
	}
	err := c.Pull(ctx, "dev1", "/nope", back)
	if !errors.Is(err, backend.ErrNotFound) || !strings.Contains(err.Error(), "No such file") {
		t.Errorf("pull missing: %v", err)
	}
	if err := c.Push(ctx, "dev1", dir, "/x"); err == nil || !strings.Contains(err.Error(), "directory") {
		t.Errorf("push dir: %v", err)
	}
}
