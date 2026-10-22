package cli

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/PiForgeZA/pooka/internal/config"
	"github.com/PiForgeZA/pooka/internal/host"
	"github.com/PiForgeZA/pooka/scripts"
)

// hostScriptsDir is where setup puts the scripts on the host. README and the
// handoffs use the same path for the manual route.
const hostScriptsDir = "/root/pooka-scripts"

const releaseRepo = "PiForgeZA/pooka"

// setupOptions are the answers. Flags seed them; prompts refine them unless --yes.
type setupOptions struct {
	Host          string
	Template      int
	Storage       string
	Bridge        string
	DiskGB        int
	Cores         int
	MemoryMB      int
	SSHPubKey     string
	BuildTemplate bool
	Daemon        bool
	DaemonURL     string
	Domain        string // DNS suffix for environment URLs (ADR 0008); default <ip>.sslip.io
	Pookad        string // local pookad binary to push instead of downloading a release
	Yes           bool
}

// sslipDomain returns the sslip.io name that resolves to the address from any
// public resolver: 192.168.68.200 becomes 192-168-68-200.sslip.io.
func sslipDomain(ip string) string {
	if ip == "" {
		return ""
	}
	return strings.ReplaceAll(ip, ".", "-") + ".sslip.io"
}

func (a *App) setupCmd() *cobra.Command {
	var o setupOptions
	c := &cobra.Command{
		Use:   "setup",
		Short: "Prepare a Proxmox host and write the pooka config (interactive)",
		Long: `Connects to the host over SSH, asks a few questions with sensible defaults,
builds the template if it is missing, installs the pookad daemon, and writes
~/.config/pooka/config.yaml. Every answer can be given as a flag; --yes takes the
flags and defaults without asking.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.runSetup(cmd.Context(), o)
		},
	}
	f := c.Flags()
	f.StringVar(&o.Host, "host", a.Cfg.Host, "ssh target for the Proxmox host")
	f.IntVar(&o.Template, "template", a.Cfg.Template, "VMID for the template container")
	f.StringVar(&o.Storage, "storage", a.Cfg.Storage, "thin storage for environments (lvmthin or zfspool)")
	f.StringVar(&o.Bridge, "bridge", a.Cfg.Bridge, "network bridge for environments")
	f.IntVar(&o.DiskGB, "disk", a.Cfg.DiskGB, "root disk size in GiB, thin")
	f.IntVar(&o.Cores, "cores", a.Cfg.Cores, "default CPU cores")
	f.IntVar(&o.MemoryMB, "memory", a.Cfg.MemoryMB, "default memory in MiB")
	f.StringVar(&o.SSHPubKey, "ssh-pubkey", detectSSHPubKey(a.Cfg.SSHPubKey), "public key to put in every environment")
	f.BoolVar(&o.BuildTemplate, "build-template", true, "build the template on the host if it is missing")
	f.BoolVar(&o.Daemon, "daemon", true, "install pookad on the host and use it as the transport")
	f.StringVar(&o.DaemonURL, "daemon-url", "", "URL clients use for pookad (default https://api.<domain>)")
	f.StringVar(&o.Domain, "domain", "", "DNS suffix for environment URLs (default <host ip>.sslip.io)")
	f.StringVar(&o.Pookad, "pookad", "", "push this local pookad binary instead of downloading the release")
	f.BoolVarP(&o.Yes, "yes", "y", false, "do not ask; use the flags and defaults")
	return c
}

// detectSSHPubKey returns the first common public key under ~/.ssh, else fallback.
func detectSSHPubKey(fallback string) string {
	for _, n := range []string{"id_ed25519.pub", "id_rsa.pub", "id_ecdsa.pub"} {
		if _, err := os.Stat(config.ExpandHome("~/.ssh/" + n)); err == nil {
			return "~/.ssh/" + n
		}
	}
	return fallback
}

// prompter asks questions on Out and reads answers from In. With yes set it
// returns the defaults without asking.
type prompter struct {
	in  *bufio.Reader
	out io.Writer
	yes bool
}

func (p *prompter) ask(label, def string) string {
	if p.yes {
		return def
	}
	fmt.Fprintf(p.out, "%s [%s]: ", label, def)
	line, _ := p.in.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return def
	}
	return line
}

func (p *prompter) askInt(label string, def int) (int, error) {
	s := p.ask(label, strconv.Itoa(def))
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%s: %q is not a positive number", label, s)
	}
	return n, nil
}

func (p *prompter) askYN(label string, def bool) bool {
	d := "y/N"
	if def {
		d = "Y/n"
	}
	s := strings.ToLower(p.ask(label, d))
	switch s {
	case "y", "yes":
		return true
	case "n", "no":
		return false
	default:
		return def
	}
}

// askChoice shows numbered options. The answer may be a number or a name.
func (p *prompter) askChoice(label string, options []string, def string) (string, error) {
	if !p.yes {
		for i, o := range options {
			fmt.Fprintf(p.out, "  %d) %s\n", i+1, o)
		}
	}
	s := p.ask(label, def)
	if n, err := strconv.Atoi(s); err == nil && n >= 1 && n <= len(options) {
		return options[n-1], nil
	}
	for _, o := range options {
		if o == s {
			return o, nil
		}
	}
	return "", fmt.Errorf("%s: %q is not one of %s", label, s, strings.Join(options, ", "))
}

// hostFacts is what setup learns from the host before asking anything.
type hostFacts struct {
	Node     string
	IP       string
	Arch     string // amd64 or arm64
	Storages []string
	Bridges  []string
}

// hostOut runs a command on the host and returns its stdout. A non-zero exit is an
// error that carries stderr, per SPEC section 14.
func hostOut(ctx context.Context, h host.Host, args ...string) (string, error) {
	var out, errb bytes.Buffer
	code, err := h.Run(ctx, host.Cmd{Args: args, Stdout: &out, Stderr: &errb})
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", fmt.Errorf("%s failed (exit %d): %s", strings.Join(args, " "), code, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

func probeHost(ctx context.Context, h host.Host) (hostFacts, error) {
	var f hostFacts
	node, err := hostOut(ctx, h, "hostname")
	if err != nil {
		return f, fmt.Errorf("cannot run commands on the host: %w", err)
	}
	f.Node = strings.TrimSpace(node)
	ips, _ := hostOut(ctx, h, "hostname", "-I")
	f.IP = strings.Fields(ips + " ")[0]
	arch, _ := hostOut(ctx, h, "uname", "-m")
	switch strings.TrimSpace(arch) {
	case "x86_64":
		f.Arch = "amd64"
	case "aarch64", "arm64":
		f.Arch = "arm64"
	default:
		return f, fmt.Errorf("unsupported host architecture %q", strings.TrimSpace(arch))
	}
	pvesm, err := hostOut(ctx, h, "pvesm", "status")
	if err != nil {
		return f, fmt.Errorf("is this a Proxmox host? %w", err)
	}
	f.Storages = thinStorages(pvesm)
	net, _ := hostOut(ctx, h, "ls", "/sys/class/net")
	for _, n := range strings.Fields(net) {
		if strings.HasPrefix(n, "vmbr") {
			f.Bridges = append(f.Bridges, n)
		}
	}
	return f, nil
}

// thinStorages parses `pvesm status` and keeps the storages that support linked
// clones and snapshots (SPEC section 5).
func thinStorages(out string) []string {
	var names []string
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || fields[0] == "Name" {
			continue
		}
		if (fields[1] == "lvmthin" || fields[1] == "zfspool") && fields[2] == "active" {
			names = append(names, fields[0])
		}
	}
	return names
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func (a *App) runSetup(ctx context.Context, o setupOptions) error {
	p := &prompter{in: bufio.NewReader(a.In), out: a.Out, yes: o.Yes}
	say := func(format string, args ...any) { fmt.Fprintf(a.Out, format+"\n", args...) }

	say("pooka setup. Press Enter to accept a default.")
	o.Host = p.ask("Proxmox host (ssh target)", o.Host)
	if a.NewHost == nil {
		return errors.New("setup: no host constructor")
	}
	h := a.NewHost(o.Host)
	say("Connecting to %s...", o.Host)
	facts, err := probeHost(ctx, h)
	if err != nil {
		return fmt.Errorf("%w\nCheck that `ssh %s true` works without a password prompt.", err, o.Host)
	}
	say("Host %s (%s, %s).", facts.Node, facts.IP, facts.Arch)

	if len(facts.Storages) == 0 {
		return errors.New("no active lvmthin or zfspool storage on the host; pooka needs one for linked clones and snapshots (SPEC section 5)")
	}
	if !contains(facts.Storages, o.Storage) {
		o.Storage = facts.Storages[0]
	}
	if o.Storage, err = p.askChoice("Thin storage for environments", facts.Storages, o.Storage); err != nil {
		return err
	}
	if len(facts.Bridges) > 0 && !contains(facts.Bridges, o.Bridge) {
		o.Bridge = facts.Bridges[0]
	}
	if len(facts.Bridges) > 1 {
		if o.Bridge, err = p.askChoice("Network bridge", facts.Bridges, o.Bridge); err != nil {
			return err
		}
	} else {
		o.Bridge = p.ask("Network bridge", o.Bridge)
	}
	if o.Template, err = p.askInt("Template VMID", o.Template); err != nil {
		return err
	}
	if o.DiskGB, err = p.askInt("Root disk per environment, GiB (thin)", o.DiskGB); err != nil {
		return err
	}
	if o.Cores, err = p.askInt("Default cores", o.Cores); err != nil {
		return err
	}
	if o.MemoryMB, err = p.askInt("Default memory, MiB", o.MemoryMB); err != nil {
		return err
	}
	o.SSHPubKey = p.ask("Public key to install in environments (empty for none)", o.SSHPubKey)

	// Template.
	exists, isTemplate, err := templateState(ctx, h, o.Template)
	if err != nil {
		return err
	}
	scriptsCopied := false
	switch {
	case exists && !isTemplate:
		return fmt.Errorf("VMID %d exists on the host but is not a template; pick another VMID with --template", o.Template)
	case exists:
		say("Template %d exists.", o.Template)
	default:
		say("Template %d does not exist.", o.Template)
		build := o.BuildTemplate && p.askYN("Build it now? This takes a few minutes", true)
		if !build {
			say("Skipped. Build it later with `pooka setup` or scripts/build-template.sh.")
		} else {
			if err := copyScripts(ctx, h); err != nil {
				return err
			}
			scriptsCopied = true
			say("Building template %d on %s from %s...", o.Template, o.Storage, hostScriptsDir+"/build-template.sh")
			env := []string{
				"POOKA_TEMPLATE_VMID=" + strconv.Itoa(o.Template),
				"POOKA_STORAGE=" + o.Storage,
				"POOKA_BRIDGE=" + o.Bridge,
				"POOKA_TEMPLATE_DISK_GB=" + strconv.Itoa(o.DiskGB),
			}
			args := append(append([]string{"env"}, env...), "bash", hostScriptsDir+"/build-template.sh")
			if err := streamHost(ctx, h, a.Out, a.Err, args...); err != nil {
				return fmt.Errorf("template build failed: %w", err)
			}
		}
	}

	// Daemon.
	cfg := a.Cfg
	cfg.Host, cfg.Template, cfg.Storage, cfg.Bridge = o.Host, o.Template, o.Storage, o.Bridge
	cfg.DiskGB, cfg.Cores, cfg.MemoryMB, cfg.SSHPubKey = o.DiskGB, o.Cores, o.MemoryMB, o.SSHPubKey
	cfg.DaemonURL, cfg.Token, cfg.TokenFile = "", "", ""
	if o.Daemon && p.askYN("Install the pookad daemon on the host and use it (recommended)", true) {
		if !scriptsCopied {
			if err := copyScripts(ctx, h); err != nil {
				return err
			}
		}
		if o.Domain == "" {
			o.Domain = sslipDomain(facts.IP)
		}
		say("Environment URLs are https://<name>-local.<domain>. Any public resolver maps\n*.%s to this host. A LAN DNS can serve a shorter name with one wildcard record.", sslipDomain(facts.IP))
		o.Domain = strings.Trim(strings.ToLower(p.ask("Domain for environment URLs", o.Domain)), ". ")
		if err := a.installDaemon(ctx, h, o, facts); err != nil {
			return err
		}
		tok, err := hostOut(ctx, h, "cat", "/etc/pooka/token")
		if err != nil {
			return fmt.Errorf("read the daemon token: %w", err)
		}
		tokPath := config.TokenPath()
		if err := os.MkdirAll(filepath.Dir(tokPath), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(tokPath, []byte(strings.TrimSpace(tok)+"\n"), 0o600); err != nil {
			return err
		}
		say("Token saved to %s.", tokPath)
		// The generated CA (ADR 0008). pookad writes it on its first start with a
		// domain; the install script restarted it a moment ago.
		var ca []byte
		for i := 0; i < 10 && len(ca) == 0; i++ {
			out, _ := hostOut(ctx, h, "cat", "/etc/pooka/tls/ca.pem")
			if strings.Contains(out, "BEGIN CERTIFICATE") {
				ca = []byte(out)
			} else {
				time.Sleep(500 * time.Millisecond)
			}
		}
		if len(ca) > 0 {
			if err := os.WriteFile(config.CAPath(), ca, 0o644); err != nil {
				return err
			}
			say("Daemon CA saved to %s.", config.CAPath())
		} else {
			say("Warning: /etc/pooka/tls/ca.pem is not on the host; is the proxy disabled in /etc/pooka/pookad.yaml? Using plain HTTP on port 7700.")
		}
		if o.DaemonURL == "" {
			if len(ca) > 0 && o.Domain != "" {
				o.DaemonURL = "https://api." + o.Domain
			} else {
				o.DaemonURL = "http://" + facts.IP + ":7700"
			}
		}
		o.DaemonURL = strings.TrimRight(p.ask("Daemon URL for clients", o.DaemonURL), "/")
		if err := checkHealth(o.DaemonURL, ca); err != nil {
			say("Warning: %s/healthz did not answer: %v. Check the address, DNS and port; the config is written anyway.", o.DaemonURL, err)
		} else {
			say("Daemon answers at %s.", o.DaemonURL)
		}
		cfg.DaemonURL, cfg.TokenFile = o.DaemonURL, tokPath
	}

	// Config.
	b, err := config.Marshal(cfg)
	if err != nil {
		return err
	}
	say("\nConfig for %s:\n%s", config.Path(), indent(string(b)))
	if _, err := os.Stat(config.Path()); err == nil && !p.askYN("The file exists. Overwrite it?", true) {
		say("Not written. The settings above are what you would need.")
		return nil
	}
	if err := config.Save(cfg); err != nil {
		return err
	}
	say("Written.")

	// Verify.
	transport := "ssh"
	if cfg.DaemonURL != "" {
		transport = "daemon"
	}
	if a.OpenWith != nil {
		if bk, err := a.OpenWith(cfg, transport); err == nil {
			if envs, err := bk.List(ctx); err == nil {
				say("Connected over %s: %d environment(s).", transport, len(envs))
			} else {
				say("Warning: `pooka ls` over %s failed: %v", transport, err)
			}
		}
	}
	say("\nNext:\n  pooka create dev1\n  pooka console dev1")
	if cfg.DaemonURL != "" && strings.HasPrefix(cfg.DaemonURL, "https://") {
		ca := config.CAPath()
		say("  pooka url dev1 --open     # the environment's URL, port 8080 inside\n\nBrowsers trust %s after a one-time step, then a restart:\n  Linux, Chrome:  certutil -d sql:$HOME/.pki/nssdb -A -t \"C,,\" -n pooka -i %s   (libnss3-tools)\n  Linux, system:  sudo cp %s /usr/local/share/ca-certificates/pooka.crt && sudo update-ca-certificates\n  macOS:          security add-trusted-cert -r trustRoot -k ~/Library/Keychains/login.keychain-db %s\n  Windows:        Import-Certificate -FilePath %s -CertStoreLocation Cert:\\CurrentUser\\Root\n  Firefox:        Settings > Privacy and Security > Certificates > Import",
			ca, ca, ca, ca, ca)
	}
	return nil
}

// templateState reports whether the VMID exists and whether it is a template.
func templateState(ctx context.Context, h host.Host, vmid int) (exists, isTemplate bool, err error) {
	var out, errb bytes.Buffer
	code, err := h.Run(ctx, host.Cmd{Args: []string{"pct", "config", strconv.Itoa(vmid)}, Stdout: &out, Stderr: &errb})
	if err != nil {
		return false, false, err
	}
	if code != 0 {
		if strings.Contains(errb.String(), "does not exist") {
			return false, false, nil
		}
		return false, false, fmt.Errorf("pct config %d: %s", vmid, strings.TrimSpace(errb.String()))
	}
	return true, regexp.MustCompile(`(?m)^template:\s*1`).MatchString(out.String()), nil
}

// copyScripts puts the embedded host scripts at hostScriptsDir, same layout as the
// repository's scripts/ directory.
func copyScripts(ctx context.Context, h host.Host) error {
	if _, err := hostOut(ctx, h, "mkdir", "-p", hostScriptsDir+"/template"); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp("", "pooka-scripts-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	return fs.WalkDir(scripts.FS, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".sh") && !strings.HasSuffix(p, ".toml") {
			return err
		}
		data, err := scripts.FS.ReadFile(p)
		if err != nil {
			return err
		}
		local := filepath.Join(tmp, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(local), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(local, data, 0o700); err != nil {
			return err
		}
		return h.CopyTo(ctx, local, path.Join(hostScriptsDir, p))
	})
}

// streamHost runs a long command on the host with its output on the terminal.
func streamHost(ctx context.Context, h host.Host, out, errw io.Writer, args ...string) error {
	code, err := h.Run(ctx, host.Cmd{Args: args, Stdout: out, Stderr: errw})
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("exit %d", code)
	}
	return nil
}

// releaseTag returns the git tag for this build when it is a release, else "".
func releaseTag() string {
	if regexp.MustCompile(`^\d+\.\d+\.\d+$`).MatchString(Version) {
		return "v" + Version
	}
	return ""
}

// fetchPookadScript downloads the pookad binary from the GitHub release that
// matches this CLI, or the latest release for a dev build. It runs on the host.
const fetchPookadScript = `set -e
tag="$1"; arch="$2"; dir="$3"
if [ -z "$tag" ]; then
  tag=$(curl -fsSL https://api.github.com/repos/` + releaseRepo + `/releases/latest | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p')
fi
[ -n "$tag" ] || { echo "could not determine the latest release" >&2; exit 1; }
ver="${tag#v}"
url="https://github.com/` + releaseRepo + `/releases/download/$tag/pooka_${ver}_linux_${arch}.tar.gz"
echo "Downloading $url"
curl -fsSL "$url" | tar -xz -C "$dir" pookad
chmod 0755 "$dir/pookad"`

func (a *App) installDaemon(ctx context.Context, h host.Host, o setupOptions, facts hostFacts) error {
	say := func(format string, args ...any) { fmt.Fprintf(a.Out, format+"\n", args...) }
	if o.Pookad != "" {
		say("Copying %s to the host...", o.Pookad)
		if err := h.CopyTo(ctx, o.Pookad, hostScriptsDir+"/pookad"); err != nil {
			return err
		}
	} else {
		tag := releaseTag()
		if tag == "" {
			say("This pooka is a dev build; fetching the latest pookad release on the host...")
		} else {
			say("Fetching pookad %s on the host...", tag)
		}
		if err := streamHost(ctx, h, a.Out, a.Err, "sh", "-c", fetchPookadScript, "sh", tag, facts.Arch, hostScriptsDir); err != nil {
			return fmt.Errorf("download pookad: %w (pass --pookad <binary> to push one instead)", err)
		}
	}
	// pookad.yaml with the chosen defaults, unless the host already has one.
	if _, err := hostOut(ctx, h, "test", "-f", "/etc/pooka/pookad.yaml"); err != nil {
		y := fmt.Sprintf("# Written by pooka setup on %s. Every key is optional.\nlisten: 0.0.0.0:7700\ntoken_file: /etc/pooka/token\norg: local\ntemplate: %d\nvmid_range: [%d, %d]\ndisk_gb: %d\ncores: %d\nmemory_mb: %d\n\n# Environment URLs (SPEC section 18): https://<name>-local.<domain> on proxy_listen.\n# Empty domain disables the proxy. tls_cert and tls_key replace the generated CA.\ndomain: %s\nproxy_listen: 0.0.0.0:443\nidle_timeout: 30m\n",
			time.Now().Format("2006-01-02"), o.Template, a.Cfg.VMIDRange[0], a.Cfg.VMIDRange[1], o.DiskGB, o.Cores, o.MemoryMB, o.Domain)
		local, err := os.CreateTemp("", "pookad-yaml-")
		if err != nil {
			return err
		}
		defer os.Remove(local.Name())
		if _, err := local.WriteString(y); err != nil {
			return err
		}
		local.Close()
		if _, err := hostOut(ctx, h, "mkdir", "-p", "-m", "0750", "/etc/pooka"); err != nil {
			return err
		}
		if err := h.CopyTo(ctx, local.Name(), "/etc/pooka/pookad.yaml"); err != nil {
			return err
		}
		say("Wrote /etc/pooka/pookad.yaml.")
	} else {
		say("/etc/pooka/pookad.yaml exists on the host; keeping it. The install script adds the domain if the file has none.")
	}
	say("Installing pookad...")
	// POOKA_DOMAIN tells install-daemon.sh which domain to add to a pookad.yaml
	// that has none (a phase 2 file). The script is the one place that edits it.
	if err := streamHost(ctx, h, a.Out, a.Err, "env", "POOKA_DOMAIN="+o.Domain, "bash", hostScriptsDir+"/install-daemon.sh", hostScriptsDir+"/pookad"); err != nil {
		return fmt.Errorf("install-daemon.sh failed: %w", err)
	}
	return nil
}

func checkHealth(url string, ca []byte) error {
	c := &http.Client{Timeout: 5 * time.Second}
	if len(ca) > 0 {
		c.Transport = transportWithCA(ca)
	}
	resp, err := c.Get(url + "/healthz")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

func indent(s string) string {
	return "  " + strings.ReplaceAll(strings.TrimRight(s, "\n"), "\n", "\n  ")
}
