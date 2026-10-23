package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/PiForgeZA/pooka/internal/backend"
	"github.com/PiForgeZA/pooka/internal/config"
)

// fakeBackend records the last call. Enough to test argument plumbing.
type fakeBackend struct {
	backend.Backend
	calls      []string
	envs       []backend.Env
	code       int
	lastCreate backend.CreateOptions
}

func (f *fakeBackend) rec(s string) { f.calls = append(f.calls, s) }

func (f *fakeBackend) Create(_ context.Context, name string, o backend.CreateOptions) (backend.Env, error) {
	f.lastCreate = o
	f.rec(fmt.Sprintf("create %s cores=%d mem=%d disk=%d", name, o.Cores, o.MemoryMB, o.DiskGB))
	return backend.Env{Name: name, VMID: 9100, IP: "10.0.0.5"}, nil
}
func (f *fakeBackend) List(context.Context) ([]backend.Env, error) { f.rec("list"); return f.envs, nil }
func (f *fakeBackend) Get(_ context.Context, name string) (backend.Env, error) {
	for _, e := range f.envs {
		if e.Name == name {
			return e, nil
		}
	}
	return backend.Env{}, backend.ErrNotFound
}
func (f *fakeBackend) Exec(_ context.Context, name string, cmd []string, o backend.ExecOptions) (int, error) {
	f.rec("exec " + name + " " + strings.Join(cmd, " ") + boolTag(" tty", o.TTY) + boolTag(" root", o.Root))
	return f.code, nil
}
func (f *fakeBackend) Destroy(_ context.Context, name string) error {
	f.rec("destroy " + name)
	return nil
}
func (f *fakeBackend) Restore(_ context.Context, name, id string) error {
	f.rec("restore " + name + " " + id)
	return nil
}
func (f *fakeBackend) Stop(_ context.Context, name string) error { f.rec("stop " + name); return nil }

func boolTag(s string, b bool) string {
	if b {
		return s
	}
	return ""
}

func newApp(fb *fakeBackend, stdin string, active string) (*App, *bytes.Buffer, *bytes.Buffer) {
	out, errb := &bytes.Buffer{}, &bytes.Buffer{}
	a := &App{Cfg: config.Default(), Backend: fb, In: strings.NewReader(stdin), Out: out, Err: errb, ActiveEnv: func() string { return active }}
	a.Cfg.SSHPubKey = "" // no key file in tests
	return a, out, errb
}

func TestExecFormsAndExitCode(t *testing.T) {
	cases := []struct {
		args   []string
		active string
		want   string
	}{
		{[]string{"exec", "dev1", "--", "echo", "hi"}, "", "exec dev1 echo hi"},
		{[]string{"exec", "--", "echo", "hi"}, "dev2", "exec dev2 echo hi"},
		{[]string{"exec", "-n", "dev3", "--", "ls"}, "", "exec dev3 ls"},
		{[]string{"exec", "-t", "--root", "dev1", "--", "bash"}, "", "exec dev1 bash tty root"},
	}
	for _, c := range cases {
		fb := &fakeBackend{code: 3}
		a, _, _ := newApp(fb, "", c.active)
		code := a.Execute(context.Background(), c.args)
		if code != 3 {
			t.Errorf("%v: exit %d, want 3", c.args, code)
		}
		if len(fb.calls) != 1 || fb.calls[0] != c.want {
			t.Errorf("%v: calls %v, want %q", c.args, fb.calls, c.want)
		}
	}
}

func TestExecWithoutEnvFails(t *testing.T) {
	a, _, errb := newApp(&fakeBackend{}, "", "")
	if code := a.Execute(context.Background(), []string{"exec", "--", "ls"}); code != 1 {
		t.Errorf("exit %d", code)
	}
	if !strings.Contains(errb.String(), "pooka use") {
		t.Errorf("stderr: %s", errb.String())
	}
}

func TestDestroyNeedsTypedNameOrForce(t *testing.T) {
	fb := &fakeBackend{}
	a, _, _ := newApp(fb, "nope\n", "")
	if code := a.Execute(context.Background(), []string{"destroy", "dev1"}); code != 1 || len(fb.calls) != 0 {
		t.Errorf("wrong name must abort: code %d calls %v", code, fb.calls)
	}
	a, _, _ = newApp(fb, "dev1\n", "")
	if code := a.Execute(context.Background(), []string{"destroy", "dev1"}); code != 0 || len(fb.calls) != 1 {
		t.Errorf("typed name must proceed: code %d calls %v", code, fb.calls)
	}
	fb = &fakeBackend{}
	a, _, _ = newApp(fb, "", "dev9")
	if code := a.Execute(context.Background(), []string{"destroy", "--force"}); code != 0 || fb.calls[0] != "destroy dev9" {
		t.Errorf("--force with active env: code %d calls %v", code, fb.calls)
	}
}

func TestRestoreArgs(t *testing.T) {
	fb := &fakeBackend{}
	a, _, _ := newApp(fb, "", "dev1")
	if code := a.Execute(context.Background(), []string{"restore", "--force", "cp-1"}); code != 0 || fb.calls[0] != "restore dev1 cp-1" {
		t.Errorf("code %d calls %v", code, fb.calls)
	}
	fb = &fakeBackend{}
	a, _, _ = newApp(fb, "", "")
	if code := a.Execute(context.Background(), []string{"restore", "--force", "dev2", "cp-2"}); code != 0 || fb.calls[0] != "restore dev2 cp-2" {
		t.Errorf("code %d calls %v", code, fb.calls)
	}
}

func TestLsMarksActive(t *testing.T) {
	fb := &fakeBackend{envs: []backend.Env{{Name: "dev1", VMID: 9100, Status: "running", IP: "10.0.0.5"}, {Name: "dev2", VMID: 9101, Status: "stopped"}}}
	a, out, _ := newApp(fb, "", "dev2")
	if code := a.Execute(context.Background(), []string{"ls"}); code != 0 {
		t.Fatal(code)
	}
	s := out.String()
	if !strings.Contains(s, "dev2 *") || !strings.Contains(s, "10.0.0.5") || !strings.Contains(s, "stopped  -") {
		t.Errorf("output:\n%s", s)
	}
}

func TestCreatePrintsSummary(t *testing.T) {
	fb := &fakeBackend{}
	a, out, _ := newApp(fb, "", "")
	if code := a.Execute(context.Background(), []string{"create", "dev1"}); code != 0 {
		t.Fatal(code)
	}
	if !strings.Contains(out.String(), "dev1 ready in") || !strings.Contains(out.String(), "VMID 9100") {
		t.Errorf("output: %s", out.String())
	}
}

func TestTransportSelection(t *testing.T) {
	cases := []struct {
		args      []string
		daemonURL string
		want      string
	}{
		{[]string{"ls"}, "", "ssh"},
		{[]string{"ls"}, "http://proxmox:7700", "daemon"},
		{[]string{"--transport", "ssh", "ls"}, "http://proxmox:7700", "ssh"},
		{[]string{"ls", "--transport", "daemon"}, "", "daemon"},
	}
	for _, c := range cases {
		fb := &fakeBackend{}
		a, _, _ := newApp(fb, "", "")
		a.Backend = nil
		a.Cfg.DaemonURL = c.daemonURL
		var opened string
		a.Open = func(tr string) (backend.Backend, error) { opened = tr; return fb, nil }
		if code := a.Execute(context.Background(), c.args); code != 0 {
			t.Errorf("%v: exit %d", c.args, code)
		}
		if opened != c.want || a.Transport != c.want {
			t.Errorf("%v with daemon_url=%q: opened %q, want %q", c.args, c.daemonURL, opened, c.want)
		}
	}
	a, _, errb := newApp(&fakeBackend{}, "", "")
	a.Backend = nil
	a.Open = func(string) (backend.Backend, error) { return nil, errors.New("no daemon configured") }
	if code := a.Execute(context.Background(), []string{"--transport", "daemon", "ls"}); code != 1 || !strings.Contains(errb.String(), "no daemon configured") {
		t.Errorf("open error must surface: code %d stderr %q", code, errb.String())
	}
}

func TestCreateOptionsPerTransport(t *testing.T) {
	// SSH: config values travel. Daemon: only flags the user set travel.
	fb := &fakeBackend{}
	a, _, _ := newApp(fb, "", "")
	a.Execute(context.Background(), []string{"create", "dev1", "--memory", "1024"})
	if fb.calls[0] != "create dev1 cores=4 mem=1024 disk=32" {
		t.Errorf("ssh: %v", fb.calls)
	}
	fb = &fakeBackend{}
	a, _, _ = newApp(fb, "", "")
	a.Cfg.DaemonURL = "http://proxmox:7700"
	a.Execute(context.Background(), []string{"create", "dev1", "--memory", "1024"})
	if fb.calls[0] != "create dev1 cores=0 mem=1024 disk=0" {
		t.Errorf("daemon: %v", fb.calls)
	}
}
