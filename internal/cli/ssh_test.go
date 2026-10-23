package cli

import (
	"bufio"
	"context"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/PiForgeZA/pooka/internal/backend"
)

// SSH by name: ip, ssh-proxy and ssh-config.

func (f *fakeBackend) Start(_ context.Context, name string) error {
	f.rec("start " + name)
	for i := range f.envs {
		if f.envs[i].Name == name {
			f.envs[i].Status = "running"
			return nil
		}
	}
	return backend.ErrNotFound
}

// echoListener answers one line with "echo:<line>" and closes.
func echoListener(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				line, _ := bufio.NewReader(c).ReadString('\n')
				c.Write([]byte("echo:" + line))
			}()
		}
	}()
	_, port, _ := net.SplitHostPort(l.Addr().String())
	n, _ := strconv.Atoi(port)
	return n
}

func sshEnvs() []backend.Env {
	return []backend.Env{
		{Name: "dev1", VMID: 9100, Status: "running", IP: "127.0.0.1"},
		{Name: "dev2", VMID: 9101, Status: "stopped", IP: "127.0.0.1"},
		{Name: "dev3", VMID: 9102, Status: "stopped"},
	}
}

func TestIPCommand(t *testing.T) {
	fb := &fakeBackend{envs: sshEnvs()}
	a, out, _ := newApp(fb, "", "")
	if code := a.Execute(context.Background(), []string{"ip", "dev1"}); code != 0 || strings.TrimSpace(out.String()) != "127.0.0.1" {
		t.Errorf("exit %d out %q", code, out.String())
	}
	a, _, errb := newApp(fb, "", "dev3")
	if code := a.Execute(context.Background(), []string{"ip"}); code != 1 || !strings.Contains(errb.String(), "pooka start dev3") {
		t.Errorf("stopped: exit %d err %q", code, errb.String())
	}
}

func TestSSHProxyForwardsAndWakes(t *testing.T) {
	port := echoListener(t)
	fb := &fakeBackend{envs: sshEnvs()}
	a, out, _ := newApp(fb, "hello\n", "")
	if code := a.Execute(context.Background(), []string{"ssh-proxy", "dev1.pooka", "--port", strconv.Itoa(port)}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if out.String() != "echo:hello\n" {
		t.Errorf("out %q", out.String())
	}
	if len(fb.calls) != 0 {
		t.Errorf("a running environment must not be started: %v", fb.calls)
	}

	// Stopped: started first, then forwarded.
	a, out, errb := newApp(fb, "wake\n", "")
	if code := a.Execute(context.Background(), []string{"ssh-proxy", "DEV2", "--port", strconv.Itoa(port)}); code != 0 {
		t.Fatalf("exit %d %s", code, errb.String())
	}
	if out.String() != "echo:wake\n" || len(fb.calls) != 1 || fb.calls[0] != "start dev2" || !strings.Contains(errb.String(), "waking dev2") {
		t.Errorf("out %q calls %v err %q", out.String(), fb.calls, errb.String())
	}

	a, _, errb = newApp(fb, "", "")
	if code := a.Execute(context.Background(), []string{"ssh-proxy", "ghost.pooka"}); code != 1 || !strings.Contains(errb.String(), "not found") {
		t.Errorf("unknown: exit %d err %q", code, errb.String())
	}
}

func TestSSHConfigPrintAndInstall(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("POOKA_CONFIG", filepath.Join(home, ".config", "pooka", "config.yaml"))
	a, out, _ := newApp(&fakeBackend{}, "", "")
	if code := a.Execute(context.Background(), []string{"ssh-config"}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	block := out.String()
	for _, want := range []string{"Host *.pooka", "User pooka", "ssh-proxy %h", "StrictHostKeyChecking accept-new", "UserKnownHostsFile " + filepath.Join(home, ".config", "pooka", "known_hosts")} {
		if !strings.Contains(block, want) {
			t.Errorf("missing %q in:\n%s", want, block)
		}
	}
	cfg := filepath.Join(home, ".ssh", "config")
	os.MkdirAll(filepath.Dir(cfg), 0o700)
	os.WriteFile(cfg, []byte("Host other\n  User x"), 0o600) // no trailing newline
	a, out, _ = newApp(&fakeBackend{}, "", "")
	if code := a.Execute(context.Background(), []string{"ssh-config", "--install"}); code != 0 || !strings.Contains(out.String(), "Added") {
		t.Fatalf("exit %d out %q", code, out.String())
	}
	b, _ := os.ReadFile(cfg)
	if !strings.HasPrefix(string(b), "Host other\n  User x\n\n# pooka") || strings.Count(string(b), "Host *.pooka") != 1 {
		t.Errorf("config after install:\n%s", b)
	}
	a, out, _ = newApp(&fakeBackend{}, "", "")
	if code := a.Execute(context.Background(), []string{"ssh-config", "--install"}); code != 0 || !strings.Contains(out.String(), "already") {
		t.Errorf("second install: exit %d out %q", code, out.String())
	}
	if b2, _ := os.ReadFile(cfg); string(b2) != string(b) {
		t.Error("second install changed the file")
	}
}
