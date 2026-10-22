package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PiForgeZA/pooka/internal/backend"
	"github.com/PiForgeZA/pooka/internal/config"
	"github.com/PiForgeZA/pooka/internal/host"
)

const pvesmOut = `Name                Type     Status           Total            Used       Available        %
local                dir     active        98497780         6626360        86821872    6.73%
local-lvm        lvmthin     active       365760512        25749540       340010971    7.04%
tank             zfspool     active       365760512        25749540       340010971    7.04%
nvme_storage         dir     active       960301072        71542084       839904616    7.45%
`

// setupHost is a Proxmox host without the template and without pookad.yaml.
func setupHost() *host.Fake {
	f := &host.Fake{}
	f.On("hostname -I", "192.168.68.200 \n")
	f.On("hostname", "proxmox\n")
	f.On("uname -m", "x86_64\n")
	f.On("pvesm status", pvesmOut)
	f.On("ls /sys/class/net", "enp5s0\nlo\nvmbr0\nvmbr1\n")
	f.OnExit("pct config 9000", 2, "Configuration file 'nodes/proxmox/lxc/9000.conf' does not exist\n")
	f.On("pct config 9001", "hostname: pooka-template\ntemplate: 1\n")
	f.On("pct config 9002", "hostname: dev1\n")
	f.OnExit("test -f /etc/pooka/pookad.yaml", 1, "")
	f.On("cat /etc/pooka/token", "tok-123\n")
	f.On("cat /etc/pooka/tls/ca.pem", "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n")
	return f
}

func TestSetupSavesCAAndDefaultsToHTTPSURL(t *testing.T) {
	fh := setupHost()
	fh.Rules = append([]host.FakeRule{{Prefix: "pct config 9000", Stdout: "template: 1\n"}}, fh.Rules...)
	a, out, _ := newSetupApp(t, fh, "")
	bin := filepath.Join(t.TempDir(), "pookad")
	os.WriteFile(bin, []byte("bin"), 0o755)
	if code := a.Execute(context.Background(), []string{"setup", "--yes", "--pookad", bin}); code != 0 {
		t.Fatalf("exit %d\n%s", code, out())
	}
	ca, err := os.ReadFile(config.CAPath())
	if err != nil || !strings.Contains(string(ca), "BEGIN CERTIFICATE") {
		t.Errorf("ca.pem: %q %v", ca, err)
	}
	cfg, _ := config.Load()
	if cfg.DaemonURL != "https://api.192-168-68-200.sslip.io" {
		t.Errorf("daemon_url: %s", cfg.DaemonURL)
	}
	if s := out(); !strings.Contains(s, "Domain for environment URLs [192-168-68-200.sslip.io]") && !strings.Contains(s, "Daemon CA saved") {
		t.Errorf("output:\n%s", s)
	}
	if s := out(); !strings.Contains(s, "update-ca-certificates") {
		t.Errorf("trust step missing:\n%s", s)
	}
}

func TestSetupAddsDomainToExistingPookadYAML(t *testing.T) {
	fh := setupHost()
	fh.Rules = append([]host.FakeRule{
		{Prefix: "pct config 9000", Stdout: "template: 1\n"},
		{Prefix: "test -f /etc/pooka/pookad.yaml", Stdout: ""},
	}, fh.Rules...)
	a, out, _ := newSetupApp(t, fh, "")
	bin := filepath.Join(t.TempDir(), "pookad")
	os.WriteFile(bin, []byte("bin"), 0o755)
	if code := a.Execute(context.Background(), []string{"setup", "--yes", "--pookad", bin, "--domain", "pooka.piforge.internal"}); code != 0 {
		t.Fatalf("exit %d\n%s", code, out())
	}
	// The install script is the one place that edits an existing pookad.yaml; setup
	// hands it the chosen domain.
	if !fh.Called("env POOKA_DOMAIN=pooka.piforge.internal bash /root/pooka-scripts/install-daemon.sh") {
		t.Errorf("domain not passed to the install script: %v", fh.Calls)
	}
	if strings.Contains(strings.Join(fh.Copies, "\n"), "->/etc/pooka/pookad.yaml") {
		t.Error("an existing pookad.yaml must not be replaced")
	}
}

func TestSslipDomain(t *testing.T) {
	if got := sslipDomain("192.168.68.200"); got != "192-168-68-200.sslip.io" {
		t.Error(got)
	}
	if got := sslipDomain(""); got != "" {
		t.Error(got)
	}
}

// newSetupApp isolates the config and token in a temp dir and serves /healthz.
func newSetupApp(t *testing.T, fh *host.Fake, stdin string) (*App, func() string, string) {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	t.Setenv("POOKA_CONFIG", cfgPath)
	t.Setenv("SPRITES_API_URL", "")
	t.Setenv("SPRITE_TOKEN", "")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
	t.Cleanup(ts.Close)
	fb := &fakeBackend{}
	a, out, _ := newApp(fb, stdin, "")
	a.Backend = nil
	a.Open = func(string) (backend.Backend, error) { return fb, nil }
	a.NewHost = func(string) host.Host { return fh }
	a.OpenWith = func(config.Config, string) (backend.Backend, error) { return fb, nil }
	return a, out.String, ts.URL
}

func TestSetupYesBuildsTemplateAndInstallsDaemon(t *testing.T) {
	fh := setupHost()
	a, out, daemonURL := newSetupApp(t, fh, "")
	code := a.Execute(context.Background(), []string{"setup", "--yes", "--host", "root@pve", "--daemon-url", daemonURL, "--ssh-pubkey", "~/.ssh/k.pub"})
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out())
	}
	wantCalls := []string{
		"env POOKA_TEMPLATE_VMID=9000 POOKA_STORAGE=local-lvm POOKA_BRIDGE=vmbr0 POOKA_TEMPLATE_DISK_GB=32 bash /root/pooka-scripts/build-template.sh",
		"env POOKA_DOMAIN=192-168-68-200.sslip.io bash /root/pooka-scripts/install-daemon.sh /root/pooka-scripts/pookad",
	}
	for _, w := range wantCalls {
		if !fh.Called(w) {
			t.Errorf("missing host call %q in %v", w, fh.Calls)
		}
	}
	var fetched bool
	for _, c := range fh.Calls {
		if strings.HasPrefix(c, "sh -c set -e") && strings.HasSuffix(c, " amd64 /root/pooka-scripts") {
			fetched = true
		}
	}
	if !fetched {
		t.Errorf("pookad must be fetched for the host arch: %v", fh.Calls)
	}
	copies := strings.Join(fh.Copies, "\n")
	for _, w := range []string{"->/root/pooka-scripts/build-template.sh", "->/root/pooka-scripts/install-daemon.sh", "->/root/pooka-scripts/template/provision.sh", "->/root/pooka-scripts/template/mise.toml", "->/etc/pooka/pookad.yaml"} {
		if !strings.Contains(copies, w) {
			t.Errorf("missing copy %q in %v", w, fh.Copies)
		}
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Host != "root@pve" || cfg.Storage != "local-lvm" || cfg.Bridge != "vmbr0" || cfg.DaemonURL != daemonURL || cfg.TokenFile != config.TokenPath() || cfg.SSHPubKey != "~/.ssh/k.pub" {
		t.Errorf("config: %+v", cfg)
	}
	tok, err := os.ReadFile(config.TokenPath())
	if err != nil || string(tok) != "tok-123\n" {
		t.Errorf("token: %q %v", tok, err)
	}
	if st, _ := os.Stat(config.TokenPath()); st.Mode().Perm() != 0o600 {
		t.Errorf("token mode %o", st.Mode().Perm())
	}
	if s := out(); !strings.Contains(s, "Daemon answers at") || !strings.Contains(s, "Connected over daemon") {
		t.Errorf("output:\n%s", s)
	}
}

func TestSetupInteractiveAnswers(t *testing.T) {
	fh := setupHost()
	// host, storage (2 = tank), bridge (vmbr1 by name), template 9001, disk, cores,
	// memory, pubkey, daemon: no, overwrite: not asked (no file yet).
	stdin := "\n2\nvmbr1\n9001\n64\n\n\n\nn\n"
	a, out, _ := newSetupApp(t, fh, stdin)
	if code := a.Execute(context.Background(), []string{"setup"}); code != 0 {
		t.Fatalf("exit %d\n%s", code, out())
	}
	if fh.Called("env POOKA") || fh.Called("bash /root/pooka-scripts/install-daemon.sh") {
		t.Errorf("template 9001 exists and daemon declined; calls %v", fh.Calls)
	}
	cfg, _ := config.Load()
	if cfg.Storage != "tank" || cfg.Bridge != "vmbr1" || cfg.Template != 9001 || cfg.DiskGB != 64 || cfg.Cores != 4 || cfg.DaemonURL != "" {
		t.Errorf("config: %+v", cfg)
	}
	if s := out(); !strings.Contains(s, "1) local-lvm") || !strings.Contains(s, "Template 9001 exists") || !strings.Contains(s, "Connected over ssh") {
		t.Errorf("output:\n%s", s)
	}
}

func TestSetupRefusesNonTemplateVMID(t *testing.T) {
	fh := setupHost()
	a, _, _ := newSetupApp(t, fh, "")
	if code := a.Execute(context.Background(), []string{"setup", "--yes", "--template", "9002", "--daemon=false"}); code != 1 {
		t.Errorf("exit %d", code)
	}
}

func TestSetupNeedsThinStorage(t *testing.T) {
	fh := setupHost()
	fh.Rules = append([]host.FakeRule{{Prefix: "pvesm status", Stdout: "Name Type Status\nlocal dir active\n"}}, fh.Rules...)
	a, _, _ := newSetupApp(t, fh, "")
	if code := a.Execute(context.Background(), []string{"setup", "--yes"}); code != 1 {
		t.Errorf("exit %d", code)
	}
	if fh.Called("env POOKA") {
		t.Error("must not build without thin storage")
	}
}

func TestSetupPushesLocalPookad(t *testing.T) {
	fh := setupHost()
	fh.Rules = append([]host.FakeRule{{Prefix: "pct config 9000", Stdout: "template: 1\n"}}, fh.Rules...)
	bin := filepath.Join(t.TempDir(), "pookad")
	os.WriteFile(bin, []byte("bin"), 0o755)
	a, out, daemonURL := newSetupApp(t, fh, "")
	if code := a.Execute(context.Background(), []string{"setup", "--yes", "--pookad", bin, "--daemon-url", daemonURL}); code != 0 {
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

func TestThinStorages(t *testing.T) {
	got := thinStorages(pvesmOut)
	if strings.Join(got, ",") != "local-lvm,tank" {
		t.Errorf("%v", got)
	}
}

func TestReleaseTag(t *testing.T) {
	old := Version
	defer func() { Version = old }()
	for v, want := range map[string]string{"0.2.1": "v0.2.1", "dev": "", "v0.1.0-5-gabc-dirty": ""} {
		Version = v
		if got := releaseTag(); got != want {
			t.Errorf("%s: got %q want %q", v, got, want)
		}
	}
}
