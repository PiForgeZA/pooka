package lxc

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/PiForgeZA/pooka/internal/backend"
	"github.com/PiForgeZA/pooka/internal/host"
)

const pctList = `VMID       Status     Lock         Name
100        running                 other
9000       stopped                 pooka-template
9100       running                 dev1
9101       stopped    snapshot     dev2
`

const confOut = `/etc/pve/lxc/100.conf:tags: calendar
/etc/pve/lxc/105.conf:#<div align='center'>
/etc/pve/lxc/9000.conf:tags: pooka-template
/etc/pve/lxc/9100.conf:#{"created"%3A"2026-10-20T12%3A00%3A00Z","pooka"%3A"dev"}
/etc/pve/lxc/9100.conf:tags: pooka
/etc/pve/lxc/9101.conf:tags: agent;pooka
proxmox
`

func newFake() *host.Fake {
	f := &host.Fake{}
	f.On("pct list", pctList)
	f.On("sh -c "+confScript, confOut)
	f.On("sh -c ls /etc/pve/lxc /etc/pve/qemu-server", "100.conf\n105.conf\n9000.conf\n9100.conf\n9101.conf\n200.conf\n")
	f.On("lxc-info -n 9100 -iH", "192.168.68.50\nfd00::1\n")
	f.On("pct config", "rootfs: local-lvm:vm-9100-disk-0,size=16G\nhostname: dev1\n")
	f.On("pvesh get /storage/local-lvm", `{"type":"lvmthin","storage":"local-lvm"}`)
	f.On("pvesm path", "/dev/pve/vm-9100-disk-0\n")
	f.On("lxc-attach -n 9102 --clear-env --keep-var TERM -- systemctl is-system-running", "running\n")
	return f
}

func newBackend(f *host.Fake) *Backend {
	return New(f, Options{Template: 9000, VMIDLow: 9100, VMIDHigh: 9199, Version: "test", Sleep: func(time.Duration) {}})
}

func TestListFiltersByTag(t *testing.T) {
	f := newFake()
	envs, err := newBackend(f).List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(envs) != 2 {
		t.Fatalf("want 2 envs, got %+v", envs)
	}
	if envs[0].Name != "dev1" || envs[0].IP != "192.168.68.50" || envs[0].Status != "running" || envs[0].Node != "proxmox" {
		t.Errorf("dev1 wrong: %+v", envs[0])
	}
	if envs[0].Created.Format(time.RFC3339) != "2026-10-20T12:00:00Z" {
		t.Errorf("dev1 created: %v", envs[0].Created)
	}
	if envs[1].Name != "dev2" || envs[1].IP != "" || envs[1].Status != "stopped" || !envs[1].Created.IsZero() {
		t.Errorf("dev2 wrong: %+v", envs[1])
	}
	if f.Called("pvesh get /cluster/resources") {
		t.Error("must not use the lagging cluster resources endpoint")
	}
}

func TestGetNotFound(t *testing.T) {
	_, err := newBackend(newFake()).Get(context.Background(), "other")
	if !errors.Is(err, backend.ErrNotFound) {
		t.Fatalf("untagged container must not be found, got %v", err)
	}
}

func TestCreateSequence(t *testing.T) {
	f := newFake()
	f.On("lxc-info -n 9102 -iH", "192.168.68.52\n")
	env, err := newBackend(f).Create(context.Background(), "dev3", backend.CreateOptions{Cores: 4, MemoryMB: 8192, DiskGB: 32, SSHPubKey: "ssh-ed25519 AAAA test"})
	if err != nil {
		t.Fatal(err)
	}
	if env.VMID != 9102 || env.IP != "192.168.68.52" {
		t.Errorf("env: %+v", env)
	}
	want := []string{
		"pct clone 9000 9102 --hostname dev3",
		"pct set 9102 --tags pooka --description ",
		"pct resize 9102 rootfs 32G",
		"pct start 9102",
		"lxc-attach -n 9102 --clear-env --keep-var TERM -- systemctl is-system-running",
		"lxc-attach -n 9102 --clear-env --keep-var TERM -- test -x /etc/pooka/firstboot.sh",
		"lxc-attach -n 9102 --clear-env --keep-var TERM -- env POOKA_NAME=dev3 POOKA_SSH_PUBKEY=ssh-ed25519 AAAA test /etc/pooka/firstboot.sh",
	}
	assertOrder(t, f.Calls, want)
	if !strings.Contains(strings.Join(f.Calls, "\n"), "--cores 4 --memory 8192") {
		t.Error("cores and memory not set")
	}
}

func TestCreateSkipsVMIDsWithConfigs(t *testing.T) {
	f := newFake()
	// 9102 has a QEMU config even though pct list does not show it.
	f.Rules = append([]host.FakeRule{{Prefix: "sh -c ls /etc/pve/lxc", Stdout: "9100.conf\n9101.conf\n9102.conf\n"}}, f.Rules...)
	f.On("lxc-attach -n 9103 --clear-env --keep-var TERM -- systemctl is-system-running", "running\n")
	env, err := newBackend(f).Create(context.Background(), "dev3", backend.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if env.VMID != 9103 {
		t.Errorf("want 9103, got %d", env.VMID)
	}
}

func TestCreateRefusesDuplicateAndBadName(t *testing.T) {
	b := newBackend(newFake())
	if _, err := b.Create(context.Background(), "dev1", backend.CreateOptions{}); !errors.Is(err, backend.ErrExists) {
		t.Errorf("want ErrExists, got %v", err)
	}
	for _, bad := range []string{"Dev", "a_b", "-x", "", strings.Repeat("a", 33)} {
		if _, err := b.Create(context.Background(), bad, backend.CreateOptions{}); err == nil {
			t.Errorf("name %q accepted", bad)
		}
	}
}

func TestCreateMissingTemplate(t *testing.T) {
	f := newFake()
	f.Rules = append([]host.FakeRule{{Prefix: "pct config 9000", Exit: 2, Stderr: "Configuration file 'nodes/proxmox/lxc/9000.conf' does not exist"}}, f.Rules...)
	_, err := newBackend(f).Create(context.Background(), "dev3", backend.CreateOptions{})
	if err == nil || !strings.Contains(err.Error(), "build-template.sh") {
		t.Errorf("want template hint, got %v", err)
	}
}

func TestCreateReadyTimeout(t *testing.T) {
	f := newFake()
	f.Rules = append([]host.FakeRule{{Prefix: "lxc-attach -n 9102 --clear-env --keep-var TERM -- systemctl is-system-running", Stdout: "starting\n"}}, f.Rules...)
	b := New(f, Options{Template: 9000, VMIDLow: 9100, VMIDHigh: 9199, ReadyWait: time.Millisecond, Sleep: func(time.Duration) {}})
	_, err := b.Create(context.Background(), "dev3", backend.CreateOptions{})
	if err == nil || !strings.Contains(err.Error(), "did not become ready") {
		t.Errorf("want timeout, got %v", err)
	}
}

func TestExecRunsAsPookaUser(t *testing.T) {
	f := newFake()
	var out bytes.Buffer
	f.On("setpriv --pdeathsig KILL lxc-attach -n 9100 --clear-env --keep-var TERM --", "hi\n")
	code, err := newBackend(f).Exec(context.Background(), "dev1", []string{"echo", "hi there"}, backend.ExecOptions{Stdout: &out, Dir: "/work", Env: []string{"TERM=xterm", "bad key=1"}})
	if err != nil || code != 0 {
		t.Fatal(code, err)
	}
	last := f.Calls[len(f.Calls)-1]
	if !strings.HasSuffix(last, "runuser -l pooka --session-command export TERM=xterm; cd /work && echo 'hi there'") {
		t.Errorf("got %q", last)
	}
	if out.String() != "hi\n" {
		t.Errorf("stdout %q", out.String())
	}
}

func TestExecRefusesStopped(t *testing.T) {
	_, err := newBackend(newFake()).Exec(context.Background(), "dev2", []string{"true"}, backend.ExecOptions{})
	if err == nil || !strings.Contains(err.Error(), "pooka start dev2") {
		t.Errorf("got %v", err)
	}
}

func TestRestoreClearsMMPOnLVMThin(t *testing.T) {
	f := newFake()
	if err := newBackend(f).Restore(context.Background(), "dev1", "cp-1"); err != nil {
		t.Fatal(err)
	}
	assertOrder(t, f.Calls, []string{
		"pct stop 9100",
		"pct rollback 9100 cp-1",
		"pvesm path local-lvm:vm-9100-disk-0",
		"tune2fs -f -E clear_mmp /dev/pve/vm-9100-disk-0",
		"pct start 9100",
	})
}

func TestRestoreSkipsMMPOnZFS(t *testing.T) {
	f := newFake()
	f.Rules = append([]host.FakeRule{
		{Prefix: "pct config", Stdout: "rootfs: tank:subvol-9101-disk-0,size=16G\n"},
		{Prefix: "pvesh get /storage/tank", Stdout: `{"type":"zfspool"}`},
	}, f.Rules...)
	if err := newBackend(f).Restore(context.Background(), "dev2", "cp-1"); err != nil {
		t.Fatal(err)
	}
	if f.Called("tune2fs") {
		t.Error("tune2fs must not run on zfs")
	}
	if f.Called("pct stop") {
		t.Error("stopped env must not be stopped again")
	}
}

func TestStopUsesShutdownWithForce(t *testing.T) {
	f := newFake()
	if err := newBackend(f).Stop(context.Background(), "dev1"); err != nil {
		t.Fatal(err)
	}
	if !f.Called("pct shutdown 9100 --timeout 10 --forceStop 1") {
		t.Errorf("calls: %v", f.Calls)
	}
}

func TestStartActsOnLiveStatus(t *testing.T) {
	f := newFake()
	if err := newBackend(f).Start(context.Background(), "dev2"); err != nil {
		t.Fatal(err)
	}
	if !f.Called("pct start 9101") {
		t.Errorf("calls: %v", f.Calls)
	}
	f = newFake()
	if err := newBackend(f).Start(context.Background(), "dev1"); err != nil {
		t.Fatal(err)
	}
	if f.Called("pct start") {
		t.Error("running env must not be started again")
	}
}

func TestDestroyStopsThenPurges(t *testing.T) {
	f := newFake()
	if err := newBackend(f).Destroy(context.Background(), "dev1"); err != nil {
		t.Fatal(err)
	}
	assertOrder(t, f.Calls, []string{"pct stop 9100", "pct destroy 9100 --purge"})
}

func TestListCheckpointsSkipsCurrentAndSortsByTime(t *testing.T) {
	f := newFake()
	// pvesh returns hash order: the newest first here, then current, then the oldest.
	f.On("pvesh get /nodes/proxmox/lxc/9100/snapshot", `[{"name":"cp-1700000900","snaptime":1700000900,"description":"later"},{"name":"current","description":"You are here!"},{"name":"cp-1700000000","snaptime":1700000000,"description":"before upgrade\n"}]`)
	cps, err := newBackend(f).ListCheckpoints(context.Background(), "dev1")
	if err != nil {
		t.Fatal(err)
	}
	if len(cps) != 2 || cps[0].ID != "cp-1700000000" || cps[0].Comment != "before upgrade" || cps[1].ID != "cp-1700000900" {
		t.Errorf("want oldest first without current, got %+v", cps)
	}
}

func TestPctErrorCarriesStderr(t *testing.T) {
	f := newFake()
	f.Rules = append([]host.FakeRule{{Prefix: "pct start 9101", Exit: 255, Stderr: "CT is locked (snapshot)"}}, f.Rules...)
	err := newBackend(f).Start(context.Background(), "dev2")
	if err == nil || !strings.Contains(err.Error(), "CT is locked") {
		t.Errorf("got %v", err)
	}
}

func TestParsePctList(t *testing.T) {
	rs := parsePctList(pctList)
	if len(rs) != 4 || rs[3].VMID != 9101 || rs[3].Status != "stopped" || rs[3].Name != "dev2" {
		t.Errorf("%+v", rs)
	}
}

func TestParseRootfs(t *testing.T) {
	v, s := parseRootfs("arch: amd64\nrootfs: local-lvm:vm-9100-disk-0,size=16G\n")
	if v != "local-lvm:vm-9100-disk-0" || s != 16 {
		t.Errorf("got %q %d", v, s)
	}
}

// assertOrder checks that each want[i] appears as a prefix of some call, in order.
func assertOrder(t *testing.T, calls, want []string) {
	t.Helper()
	i := 0
	for _, c := range calls {
		if i < len(want) && strings.HasPrefix(c, want[i]) {
			i++
		}
	}
	if i != len(want) {
		t.Errorf("missing %q in order.\ncalls:\n  %s", want[i], strings.Join(calls, "\n  "))
	}
}

func TestExecTTYKeepsSession(t *testing.T) {
	f := newFake()
	f.On("setpriv --pdeathsig KILL lxc-attach -n 9100 --clear-env --keep-var TERM --", "")
	if _, err := newBackend(f).Exec(context.Background(), "dev1", []string{"bash", "-l"}, backend.ExecOptions{TTY: true}); err != nil {
		t.Fatal(err)
	}
	last := f.Calls[len(f.Calls)-1]
	if !strings.Contains(last, "--session-command bash -l") {
		t.Errorf("tty exec must keep the session: %s", last)
	}
	f = newFake()
	f.On("setpriv --pdeathsig KILL lxc-attach -n 9100 --clear-env --keep-var TERM --", "")
	if _, err := newBackend(f).Exec(context.Background(), "dev1", []string{"true"}, backend.ExecOptions{}); err != nil {
		t.Fatal(err)
	}
	if last := f.Calls[len(f.Calls)-1]; !strings.Contains(last, "runuser -l pooka --session-command true") {
		t.Errorf("non-tty exec must keep the session: %s", last)
	}
}

// Phase 3: settings live in the description JSON (ADR 0008, 0009).

const confOutSettings = `/etc/pve/lxc/9100.conf:#{"created"%3A"2026-10-20T12%3A00%3A00Z","pooka"%3A"dev","url_auth"%3A"public","idle_timeout"%3A"0s"}
/etc/pve/lxc/9100.conf:tags: pooka
/etc/pve/lxc/9101.conf:#{"created"%3A"2026-10-20T12%3A00%3A00Z","pooka"%3A"dev","idle_timeout"%3A"1h0m0s"}
/etc/pve/lxc/9101.conf:tags: pooka
proxmox
`

func TestListReadsSettingsFromDescription(t *testing.T) {
	f := &host.Fake{}
	f.On("pct list", pctList)
	f.On("sh -c "+confScript, confOutSettings)
	envs, err := newBackend(f).List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(envs) != 2 {
		t.Fatalf("want 2 envs, got %+v", envs)
	}
	if envs[0].URLAuth != "public" || envs[0].IdleTimeout == nil || *envs[0].IdleTimeout != 0 {
		t.Errorf("dev1 settings: %+v", envs[0].Settings)
	}
	if envs[1].URLAuth != "" || envs[1].IdleTimeout == nil || *envs[1].IdleTimeout != time.Hour {
		t.Errorf("dev2 settings: %+v", envs[1].Settings)
	}
}

func TestUpdateMergesIntoDescription(t *testing.T) {
	f := newFake()
	b := newBackend(f)
	if err := b.Update(context.Background(), "dev1", backend.Settings{URLAuth: "public"}); err != nil {
		t.Fatal(err)
	}
	// The description comes from the inventory's conf scan; no `pct config` call.
	want := `pct set 9100 --description {"created":"2026-10-20T12:00:00Z","pooka":"dev","url_auth":"public"}`
	if !f.Called(want) {
		t.Errorf("want %q in calls:\n%s", want, strings.Join(f.Calls, "\n"))
	}
	if f.Called("pct config 9100") {
		t.Error("Update must not re-read the config")
	}
	d := 90 * time.Minute
	if err := b.Update(context.Background(), "dev1", backend.Settings{IdleTimeout: &d}); err != nil {
		t.Fatal(err)
	}
	want = `pct set 9100 --description {"created":"2026-10-20T12:00:00Z","idle_timeout":"1h30m0s","pooka":"dev"}`
	if !f.Called(want) {
		t.Errorf("want %q in calls:\n%s", want, strings.Join(f.Calls, "\n"))
	}
	// dev2 has a tag line but no pooka description: Update starts a fresh object.
	if err := b.Update(context.Background(), "dev2", backend.Settings{URLAuth: "public"}); err != nil {
		t.Fatal(err)
	}
	if want := `pct set 9101 --description {"url_auth":"public"}`; !f.Called(want) {
		t.Errorf("want %q in calls:\n%s", want, strings.Join(f.Calls, "\n"))
	}
	if err := b.Update(context.Background(), "dev1", backend.Settings{URLAuth: "nope"}); err == nil {
		t.Error("want an error for an invalid auth value")
	}
	if err := b.Update(context.Background(), "ghost", backend.Settings{URLAuth: "public"}); !errors.Is(err, backend.ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

func TestCreateWritesIdleTimeout(t *testing.T) {
	f := newFake()
	f.On("lxc-info -n 9102 -iH", "192.168.68.52\n")
	d := time.Duration(0)
	if _, err := newBackend(f).Create(context.Background(), "dev3", backend.CreateOptions{IdleTimeout: &d}); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range f.Calls {
		if strings.HasPrefix(c, "pct set 9102 ") && strings.Contains(c, `"idle_timeout":"0s"`) {
			found = true
		}
	}
	if !found {
		t.Errorf("idle_timeout not in the description:\n%s", strings.Join(f.Calls, "\n"))
	}
}
