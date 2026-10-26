package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/PiForgeZA/pooka/internal/host"
)

// pooka update and pooka version. The CLI side repeats install.sh in Go:
// download the release archive for this OS and architecture, verify it against
// checksums.txt, and rename the new binary over the running one. The daemon
// side is the setup path: fetch or push pookad on the host, then
// install-daemon.sh, which keeps pookad.yaml and restarts the unit.

const pookadBin = "/usr/local/bin/pookad"

type updateOptions struct {
	CLI, Daemon bool
	Version     string // a release tag; "" means the latest
	Pookad      string // a local pookad binary to push instead of the release
}

func (a *App) updateCmd() *cobra.Command {
	var o updateOptions
	c := &cobra.Command{
		Use:     "update",
		Aliases: []string{"upgrade"},
		Short:   "Update the pooka CLI and the pookad daemon to a release",
		Long: `Update the pooka CLI and, when a host is configured, the pookad daemon.
Without flags both go to the latest release. --cli and --daemon select one.
--version pins a release. --pookad pushes a local daemon binary, as setup does.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.runUpdate(cmd.Context(), o)
		},
	}
	c.Flags().BoolVar(&o.CLI, "cli", false, "update only the CLI")
	c.Flags().BoolVar(&o.Daemon, "daemon", false, "update only the daemon on the host")
	c.Flags().StringVar(&o.Version, "version", "", "release to install, for example v0.5.0 (default: the latest)")
	c.Flags().StringVar(&o.Pookad, "pookad", "", "push this local pookad binary instead of the release")
	return c
}

func (a *App) versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Show the versions of the CLI and the daemon",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			fmt.Fprintf(a.Out, "pooka %s\n", Version)
			v, err := a.daemonVersion(cmd.Context())
			switch {
			case err != nil:
				fmt.Fprintf(a.Out, "pookad unknown: %v\n", err)
			case v == "":
				fmt.Fprintln(a.Out, "pookad not configured: no daemon_url or host in the config")
			default:
				fmt.Fprintf(a.Out, "pookad %s\n", v)
			}
			return nil
		},
	}
}

func (a *App) runUpdate(ctx context.Context, o updateOptions) error {
	doCLI := o.CLI || !o.Daemon
	doDaemon := o.Daemon || !o.CLI && a.Cfg.Host != ""
	if o.Daemon && a.Cfg.Host == "" {
		return errors.New("no host in the config; run pooka setup first")
	}
	tag := o.Version
	if tag != "" && !strings.HasPrefix(tag, "v") {
		tag = "v" + tag
	}
	if tag == "" && (o.Pookad == "" || doCLI) {
		var err error
		if tag, err = a.latestRelease(ctx); err != nil {
			return fmt.Errorf("find the latest release: %w", err)
		}
	}
	// The daemon goes first: the scripts it needs are embedded in the binary
	// that runs now, and a failed CLI replacement must not leave the host behind.
	if doDaemon {
		if err := a.updateDaemon(ctx, tag, o.Pookad); err != nil {
			return err
		}
	}
	if doCLI {
		if err := a.updateCLI(ctx, tag); err != nil {
			return err
		}
	}
	return nil
}

// latestRelease asks the GitHub API for the newest release tag.
func (a *App) latestRelease(ctx context.Context) (string, error) {
	u := a.ReleaseAPI
	if u == "" {
		u = "https://api.github.com/repos/" + releaseRepo + "/releases/latest"
	}
	body, err := fetch(ctx, u)
	if err != nil {
		return "", err
	}
	var rel struct {
		Tag string `json:"tag_name"`
	}
	if err := json.Unmarshal(body, &rel); err != nil || rel.Tag == "" {
		return "", errors.New("no tag_name in the release response")
	}
	return rel.Tag, nil
}

func (a *App) updateCLI(ctx context.Context, tag string) error {
	say := func(format string, args ...any) { fmt.Fprintf(a.Out, format+"\n", args...) }
	if releaseTag() == tag {
		say("pooka is already at %s.", tag)
		return nil
	}
	exe, err := a.executable()
	if err != nil {
		return err
	}
	base := a.ReleaseDownload
	if base == "" {
		base = "https://github.com/" + releaseRepo + "/releases/download"
	}
	name := fmt.Sprintf("pooka_%s_%s_%s.tar.gz", strings.TrimPrefix(tag, "v"), runtime.GOOS, runtime.GOARCH)
	say("Downloading %s/%s/%s...", base, tag, name)
	archive, err := fetch(ctx, base+"/"+tag+"/"+name)
	if err != nil {
		return err
	}
	sums, err := fetch(ctx, base+"/"+tag+"/checksums.txt")
	if err != nil {
		return err
	}
	if err := verifyChecksum(archive, name, string(sums)); err != nil {
		return err
	}
	bin, err := extractFromTarGz(archive, "pooka")
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	// Write next to the running binary, then rename over it: the rename is
	// atomic and a running process keeps its old inode.
	dir := filepath.Dir(exe)
	tmp, err := os.CreateTemp(dir, ".pooka-update-")
	if err != nil {
		return fmt.Errorf("cannot write to %s: %w. Reinstall there with sudo, or install.sh --bin-dir to a writable directory", dir, err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(bin); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o755); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), exe); err != nil {
		return fmt.Errorf("cannot replace %s: %w", exe, err)
	}
	say("pooka: %s -> %s (%s)", Version, tag, exe)
	return nil
}

func (a *App) updateDaemon(ctx context.Context, tag, pookad string) error {
	say := func(format string, args ...any) { fmt.Fprintf(a.Out, format+"\n", args...) }
	h := a.NewHost(a.Cfg.Host)
	before, err := a.daemonVersion(ctx)
	if err != nil {
		before = "unknown"
	}
	arch, err := hostArch(ctx, h)
	if err != nil {
		return err
	}
	say("Refreshing the scripts on the host...")
	if err := copyScripts(ctx, h); err != nil {
		return err
	}
	if err := a.putPookad(ctx, h, pookad, tag, arch); err != nil {
		return err
	}
	say("Installing pookad...")
	if err := streamHost(ctx, h, a.Out, a.Err, "bash", hostScriptsDir+"/install-daemon.sh", hostScriptsDir+"/pookad"); err != nil {
		return fmt.Errorf("install-daemon.sh failed: %w", err)
	}
	after := a.waitDaemonVersion(ctx, 15*time.Second)
	say("pookad: %s -> %s", before, after)
	return nil
}

// daemonVersion returns the running daemon's version: from GET /healthz over
// daemon_url, else from pookad --version over SSH. "" means no daemon is
// configured.
func (a *App) daemonVersion(ctx context.Context) (string, error) {
	if a.Cfg.DaemonURL != "" {
		c := &http.Client{Timeout: 5 * time.Second}
		if ca, err := a.Cfg.ReadCA(); err == nil && len(ca) > 0 {
			c.Transport = transportWithCA(ca)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.Cfg.DaemonURL+"/healthz", nil)
		if err != nil {
			return "", err
		}
		resp, err := c.Do(req)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		var h struct {
			Version string `json:"version"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&h); err != nil {
			return "", fmt.Errorf("%s/healthz: %w", a.Cfg.DaemonURL, err)
		}
		return h.Version, nil
	}
	if a.Cfg.Host == "" {
		return "", nil
	}
	out, err := hostOut(ctx, a.NewHost(a.Cfg.Host), pookadBin, "--version")
	if err != nil {
		return "", err
	}
	return strings.TrimPrefix(strings.TrimSpace(out), "pookad version "), nil
}

// daemonPoll is the pause between version checks while the daemon restarts.
// Tests shorten it.
var daemonPoll = time.Second

// waitDaemonVersion polls the daemon URL until it answers after a restart.
// Over SSH, pookad --version does not depend on the unit, so one check is
// enough.
func (a *App) waitDaemonVersion(ctx context.Context, timeout time.Duration) string {
	deadline := time.Now().Add(timeout)
	for {
		v, err := a.daemonVersion(ctx)
		if err == nil && v != "" {
			return v
		}
		if a.Cfg.DaemonURL == "" || time.Now().After(deadline) || ctx.Err() != nil {
			return "unknown"
		}
		time.Sleep(daemonPoll)
	}
}

// putPookad places the pookad binary in hostScriptsDir: a local file pushed
// over SSH, or the release archive fetched on the host.
func (a *App) putPookad(ctx context.Context, h host.Host, pookad, tag, arch string) error {
	say := func(format string, args ...any) { fmt.Fprintf(a.Out, format+"\n", args...) }
	if pookad != "" {
		say("Copying %s to the host...", pookad)
		return h.CopyTo(ctx, pookad, hostScriptsDir+"/pookad")
	}
	if tag == "" {
		say("This pooka is a dev build; fetching the latest pookad release on the host...")
	} else {
		say("Fetching pookad %s on the host...", tag)
	}
	if err := streamHost(ctx, h, a.Out, a.Err, "sh", "-c", fetchPookadScript, "sh", tag, arch, hostScriptsDir); err != nil {
		return fmt.Errorf("download pookad: %w (pass --pookad <binary> to push one instead)", err)
	}
	return nil
}

// hostArch maps the host's uname -m to a release architecture.
func hostArch(ctx context.Context, h host.Host) (string, error) {
	arch, err := hostOut(ctx, h, "uname", "-m")
	if err != nil {
		return "", fmt.Errorf("cannot run commands on the host: %w", err)
	}
	switch strings.TrimSpace(arch) {
	case "x86_64":
		return "amd64", nil
	case "aarch64", "arm64":
		return "arm64", nil
	}
	return "", fmt.Errorf("unsupported host architecture %q", strings.TrimSpace(arch))
}

func (a *App) executable() (string, error) {
	if a.Executable != nil {
		return a.Executable()
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(exe)
}

func fetch(ctx context.Context, u string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "pooka/"+Version)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: HTTP %d", u, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 256<<20))
}

// verifyChecksum checks data against the sha256 line for name in checksums.txt.
func verifyChecksum(data []byte, name, sums string) error {
	for _, line := range strings.Split(sums, "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && f[1] == name {
			sum := sha256.Sum256(data)
			if hex.EncodeToString(sum[:]) != strings.ToLower(f[0]) {
				return fmt.Errorf("checksum mismatch for %s: the download does not match checksums.txt", name)
			}
			return nil
		}
	}
	return fmt.Errorf("%s is not listed in checksums.txt", name)
}

func extractFromTarGz(archive []byte, member string) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil, fmt.Errorf("no %s in the archive", member)
		}
		if err != nil {
			return nil, err
		}
		if filepath.Base(hdr.Name) == member && hdr.Typeflag == tar.TypeReg {
			return io.ReadAll(tr)
		}
	}
}
