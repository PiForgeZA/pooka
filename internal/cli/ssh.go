package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/PiForgeZA/pooka/internal/config"
)

// SSH by name. `ssh <name>.pooka` reaches an environment through a ProxyCommand
// that asks pooka for the address and wakes the environment when it is stopped.
// One Host entry in ~/.ssh/config, written by `pooka ssh-config --install` or by
// `pooka setup`, serves every environment. SPEC section 10.

// SSHSuffix is the host name suffix that selects the ProxyCommand.
const SSHSuffix = ".pooka"

// sshWait bounds a wake: start plus waiting for sshd.
const sshWait = 30 * time.Second

// knownHostsPath is the known_hosts file for environments, next to the config.
// Environments come and go under the same name, so they do not share the user's
// own file.
func knownHostsPath() string {
	return filepath.Join(filepath.Dir(config.Path()), "known_hosts")
}

// sshConfigPath is the user's ssh client config.
func sshConfigPath() string {
	return config.ExpandHome("~/.ssh/config")
}

// SSHConfigBlock returns the Host entry for ~/.ssh/config. The ProxyCommand names
// this binary by absolute path, because ssh does not run a login shell.
func SSHConfigBlock() string {
	bin, err := os.Executable()
	if err != nil || bin == "" {
		bin = "pooka"
	}
	return fmt.Sprintf(`# pooka: ssh <name>%s reaches an environment by name and wakes it when stopped.
Host *%s
  User pooka
  ProxyCommand %s ssh-proxy %%h
  StrictHostKeyChecking accept-new
  UserKnownHostsFile %s
`, SSHSuffix, SSHSuffix, bin, knownHostsPath())
}

// installSSHConfig appends the block to ~/.ssh/config unless a Host *.pooka entry
// exists. It returns true when it wrote.
func installSSHConfig() (bool, error) {
	p := sshConfigPath()
	b, err := os.ReadFile(p)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	if strings.Contains(string(b), "Host *"+SSHSuffix) {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return false, err
	}
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return false, err
	}
	defer f.Close()
	sep := ""
	if len(b) > 0 && !strings.HasSuffix(string(b), "\n") {
		sep = "\n"
	}
	if len(b) > 0 {
		sep += "\n"
	}
	_, err = f.WriteString(sep + SSHConfigBlock())
	return err == nil, err
}

// forgetHostKey drops the known_hosts entry of a destroyed environment, so the
// next one under that name is accepted as new. Best effort: no file, no action.
func forgetHostKey(name string) {
	kh := knownHostsPath()
	if _, err := os.Stat(kh); err != nil {
		return
	}
	cmd := exec.Command("ssh-keygen", "-R", name+SSHSuffix, "-f", kh)
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	_ = cmd.Run()
}

func (a *App) ipCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "ip [name]",
		Short: "Print the environment's IP address",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, _, err := a.envName(cmd, args)
			if err != nil {
				return err
			}
			env, err := a.Backend.Get(cmd.Context(), name)
			if err != nil {
				return err
			}
			if env.IP == "" {
				if env.Status != "running" {
					return fmt.Errorf("%s is stopped: run `pooka start %s`, or use `ssh %s%s` which wakes it", name, name, name, SSHSuffix)
				}
				return fmt.Errorf("%s has no IP address yet", name)
			}
			fmt.Fprintln(a.Out, env.IP)
			return nil
		},
	}
	addNameFlag(c)
	return c
}

func (a *App) sshProxyCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "ssh-proxy <name>[" + SSHSuffix + "]",
		Short: "Connect stdin and stdout to port 22 of an environment (for ssh ProxyCommand)",
		Long: `Looks the environment up, starts it when it is stopped, waits for sshd, and
forwards the connection. Not meant to be run by hand: ~/.ssh/config uses it as the
ProxyCommand for Host *` + SSHSuffix + `. Print or install that entry with pooka ssh-config.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := strings.TrimSuffix(strings.ToLower(args[0]), SSHSuffix)
			port, _ := cmd.Flags().GetInt("port")
			conn, err := a.dialEnv(cmd.Context(), name, port)
			if err != nil {
				return err
			}
			defer conn.Close()
			done := make(chan struct{})
			go func() {
				_, _ = io.Copy(conn, a.In)
				if tc, ok := conn.(*net.TCPConn); ok {
					_ = tc.CloseWrite()
				}
				close(done)
			}()
			_, err = io.Copy(a.Out, conn)
			conn.Close()
			<-done
			if err != nil && !errors.Is(err, net.ErrClosed) {
				return err
			}
			return nil
		},
	}
	c.Flags().Int("port", 22, "port inside the environment")
	return c
}

// dialEnv connects to a port in the environment. A stopped environment is
// started first; then the port is polled until it answers or sshWait passes.
func (a *App) dialEnv(ctx context.Context, name string, port int) (net.Conn, error) {
	env, err := a.Backend.Get(ctx, name)
	if err != nil {
		return nil, err
	}
	if env.Status != "running" {
		fmt.Fprintf(a.Err, "pooka: waking %s\n", name)
		if err := a.Backend.Start(ctx, name); err != nil {
			return nil, err
		}
	}
	deadline := time.Now().Add(sshWait)
	for {
		if env.IP == "" || env.Status != "running" {
			if env, err = a.Backend.Get(ctx, name); err != nil {
				return nil, err
			}
		}
		if env.IP != "" {
			d := net.Dialer{Timeout: 2 * time.Second}
			conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(env.IP, strconv.Itoa(port)))
			if err == nil {
				return conn, nil
			}
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("%s: port %d did not answer within %s", name, port, sshWait)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func (a *App) sshConfigCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "ssh-config",
		Short: "Print the ~/.ssh/config entry that makes `ssh <name>" + SSHSuffix + "` work; --install appends it",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if install, _ := cmd.Flags().GetBool("install"); install {
				wrote, err := installSSHConfig()
				if err != nil {
					return err
				}
				if wrote {
					fmt.Fprintf(a.Out, "Added Host *%s to %s.\n", SSHSuffix, sshConfigPath())
				} else {
					fmt.Fprintf(a.Out, "%s already has a Host *%s entry.\n", sshConfigPath(), SSHSuffix)
				}
				return nil
			}
			fmt.Fprint(a.Out, SSHConfigBlock())
			return nil
		},
	}
	c.Flags().Bool("install", false, "append the entry to ~/.ssh/config if it is not there")
	return c
}
