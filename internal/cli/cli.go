// Package cli defines the pooka command tree. See SPEC.md section 10.
package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/PiForgeZA/pooka/internal/backend"
	"github.com/PiForgeZA/pooka/internal/backend/httpapi"
	"github.com/PiForgeZA/pooka/internal/backend/lxc"
	"github.com/PiForgeZA/pooka/internal/config"
	"github.com/PiForgeZA/pooka/internal/host"
)

// Version is set by the linker at build time.
var Version = "dev"

// App holds what commands need. Tests inject a fake backend.
type App struct {
	Cfg config.Config
	// Backend is nil until the root command's PersistentPreRun opens one with Open.
	// Tests set it directly.
	Backend backend.Backend
	// Open returns the backend for a transport, "ssh" or "daemon".
	Open func(transport string) (backend.Backend, error)
	// Transport is the one in use, set before a command runs.
	Transport string
	In        io.Reader
	Out       io.Writer
	Err       io.Writer
	// ActiveEnv returns the name from ./.pooka. Tests override it.
	ActiveEnv func() string
}

// NewApp wires the real config and the backend opener.
func NewApp() (*App, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	a := &App{Cfg: cfg, In: os.Stdin, Out: os.Stdout, Err: os.Stderr, ActiveEnv: config.ActiveEnv}
	a.Open = func(t string) (backend.Backend, error) { return openBackend(cfg, t) }
	return a, nil
}

// openBackend builds the SSH+LXC backend or the HTTP client. ADR 0005.
func openBackend(cfg config.Config, transport string) (backend.Backend, error) {
	debug := os.Getenv("POOKA_DEBUG") != ""
	switch transport {
	case "ssh":
		var h host.Host = host.NewSSH(cfg.Host)
		if debug {
			h = host.Logged{Host: h, Out: os.Stderr}
		}
		return lxc.New(h, lxc.Options{Template: cfg.Template, VMIDLow: cfg.VMIDRange[0], VMIDHigh: cfg.VMIDRange[1], Version: Version}), nil
	case "daemon":
		if cfg.DaemonURL == "" {
			return nil, fmt.Errorf("no daemon configured: set daemon_url in %s or export SPRITES_API_URL, or use --transport ssh", config.Path())
		}
		tok, err := cfg.DaemonToken()
		if err != nil {
			return nil, err
		}
		c := httpapi.New(cfg.DaemonURL, tok)
		c.UserAgent = "pooka/" + Version
		c.Progress = os.Stderr
		if debug {
			c.Debug = os.Stderr
		}
		return c, nil
	default:
		return nil, fmt.Errorf("unknown transport %q: use ssh or daemon", transport)
	}
}

// transport picks the transport from --transport, else from the config: daemon when
// daemon_url is set, otherwise ssh.
func (a *App) transport(cmd *cobra.Command) string {
	if t, _ := cmd.Flags().GetString("transport"); t != "" {
		return t
	}
	if a.Cfg.DaemonURL != "" {
		return "daemon"
	}
	return "ssh"
}

// Root builds the command tree.
func (a *App) Root() *cobra.Command {
	root := &cobra.Command{
		Use:           "pooka",
		Short:         "Fast, disposable Linux environments on your own Proxmox host",
		Version:       Version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetIn(a.In)
	root.SetOut(a.Out)
	root.SetErr(a.Err)
	root.PersistentFlags().String("transport", "", "ssh or daemon (default: daemon when daemon_url is set, else ssh)")
	root.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		a.Transport = a.transport(cmd)
		if a.Backend != nil {
			return nil
		}
		if a.Open == nil {
			return errors.New("no backend configured")
		}
		b, err := a.Open(a.Transport)
		if err != nil {
			return err
		}
		a.Backend = b
		return nil
	}
	root.AddCommand(
		a.createCmd(), a.lsCmd(), a.execCmd(), a.consoleCmd(),
		a.pushCmd(), a.pullCmd(),
		a.checkpointCmd(), a.restoreCmd(),
		a.startCmd(), a.stopCmd(), a.destroyCmd(), a.useCmd(),
	)
	return root
}

// Execute runs the CLI and returns the process exit code.
func (a *App) Execute(ctx context.Context, args []string) int {
	root := a.Root()
	root.SetArgs(args)
	err := root.ExecuteContext(ctx)
	var ec exitCodeError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &ec):
		return int(ec)
	default:
		fmt.Fprintln(a.Err, "error:", err)
		return 1
	}
}

// exitCodeError carries the exit code of a command run inside an environment.
type exitCodeError int

func (e exitCodeError) Error() string { return fmt.Sprintf("exit %d", int(e)) }

// envName resolves the environment from a positional arg, -n, or ./.pooka.
func (a *App) envName(cmd *cobra.Command, args []string) (string, []string, error) {
	if n, _ := cmd.Flags().GetString("name"); n != "" {
		return n, args, nil
	}
	if len(args) > 0 && args[0] != "" {
		return args[0], args[1:], nil
	}
	if n := a.ActiveEnv(); n != "" {
		return n, args, nil
	}
	return "", args, errors.New("no environment given: pass a name, -n <name>, or run `pooka use <name>`")
}

func addNameFlag(c *cobra.Command) {
	c.Flags().StringP("name", "n", "", "environment name (alternative to the positional name)")
}

// confirm asks the user to type name, unless --force. SPEC section 10 rules.
func (a *App) confirm(cmd *cobra.Command, action, name string) error {
	if f, _ := cmd.Flags().GetBool("force"); f {
		return nil
	}
	fmt.Fprintf(a.Out, "This will %s %s. Type the name to continue: ", action, name)
	line, _ := bufio.NewReader(a.In).ReadString('\n')
	if strings.TrimSpace(line) != name {
		return errors.New("aborted: name did not match")
	}
	return nil
}

func (a *App) createCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "create <name>",
		Short: "Create and start a new environment from the template",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			key, err := a.Cfg.ReadSSHPubKey()
			if err != nil {
				return err
			}
			cores, _ := cmd.Flags().GetInt("cores")
			mem, _ := cmd.Flags().GetInt("memory")
			disk, _ := cmd.Flags().GetInt("disk")
			if a.Transport == "daemon" {
				// Over the daemon, a flag the user did not set means "the daemon's
				// default", not the local config value. ADR 0005.
				if !cmd.Flags().Changed("cores") {
					cores = 0
				}
				if !cmd.Flags().Changed("memory") {
					mem = 0
				}
				if !cmd.Flags().Changed("disk") {
					disk = 0
				}
			}
			start := time.Now()
			env, err := a.Backend.Create(cmd.Context(), args[0], backend.CreateOptions{Cores: cores, MemoryMB: mem, DiskGB: disk, SSHPubKey: key})
			if err != nil {
				return err
			}
			fmt.Fprintf(a.Out, "%s ready in %.1fs (VMID %d, IP %s)\n", env.Name, time.Since(start).Seconds(), env.VMID, orDash(env.IP))
			fmt.Fprintf(a.Out, "  pooka console %s\n", env.Name)
			if env.IP != "" && key != "" {
				fmt.Fprintf(a.Out, "  ssh pooka@%s\n", env.IP)
			}
			return nil
		},
	}
	c.Flags().Int("cores", a.Cfg.Cores, "CPU cores (over the daemon: its default unless set)")
	c.Flags().Int("memory", a.Cfg.MemoryMB, "memory in MiB (over the daemon: its default unless set)")
	c.Flags().Int("disk", a.Cfg.DiskGB, "root disk size in GiB, thin (over the daemon: its default unless set)")
	return c
}

func (a *App) lsCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List environments",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			envs, err := a.Backend.List(cmd.Context())
			if err != nil {
				return err
			}
			active := a.ActiveEnv()
			w := tabwriter.NewWriter(a.Out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tVMID\tSTATUS\tIP")
			for _, e := range envs {
				mark := ""
				if e.Name == active {
					mark = " *"
				}
				fmt.Fprintf(w, "%s%s\t%d\t%s\t%s\n", e.Name, mark, e.VMID, e.Status, orDash(e.IP))
			}
			return w.Flush()
		},
	}
}

func (a *App) execCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "exec [name] -- <command...>",
		Aliases: []string{"x"},
		Short:   "Run a command inside an environment",
		RunE: func(cmd *cobra.Command, args []string) error {
			name, rest, err := a.envNameForExec(cmd, args)
			if err != nil {
				return err
			}
			if len(rest) == 0 {
				return errors.New("no command given; use: pooka exec <name> -- <command>")
			}
			tty, _ := cmd.Flags().GetBool("tty")
			root, _ := cmd.Flags().GetBool("root")
			dir, _ := cmd.Flags().GetString("dir")
			code, err := a.Backend.Exec(cmd.Context(), name, rest, backend.ExecOptions{Stdin: a.In, Stdout: a.Out, Stderr: a.Err, TTY: tty, Root: root, Dir: dir})
			if err != nil {
				return err
			}
			if code != 0 {
				return exitCodeError(code)
			}
			return nil
		},
	}
	addNameFlag(c)
	c.Flags().BoolP("tty", "t", false, "allocate a TTY")
	c.Flags().Bool("root", false, "run as root instead of the pooka user")
	c.Flags().String("dir", "", "working directory inside the environment")
	return c
}

// envNameForExec handles `pooka exec name -- cmd` and `pooka exec -- cmd` (active env).
func (a *App) envNameForExec(cmd *cobra.Command, args []string) (string, []string, error) {
	dash := cmd.ArgsLenAtDash()
	if n, _ := cmd.Flags().GetString("name"); n != "" {
		if dash >= 0 {
			return n, args[dash:], nil
		}
		return n, args, nil
	}
	switch {
	case dash == 1:
		return args[0], args[1:], nil
	case dash == 0 || dash < 0:
		if n := a.ActiveEnv(); n != "" {
			return n, args, nil
		}
		if dash < 0 && len(args) > 1 {
			return args[0], args[1:], nil
		}
		return "", nil, errors.New("no environment given: pass a name, -n <name>, or run `pooka use <name>`")
	default:
		return "", nil, errors.New("give one environment name before --")
	}
}

func (a *App) consoleCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "console [name]",
		Aliases: []string{"c"},
		Short:   "Open an interactive shell as the pooka user",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, _, err := a.envName(cmd, args)
			if err != nil {
				return err
			}
			code, err := a.Backend.Exec(cmd.Context(), name, []string{"bash", "-l"}, backend.ExecOptions{TTY: true})
			if err != nil {
				return err
			}
			if code != 0 {
				return exitCodeError(code)
			}
			return nil
		},
	}
	addNameFlag(c)
	return c
}

func (a *App) pushCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "push [name] <local> <remote>",
		Short: "Copy a local file into an environment",
		Args:  cobra.RangeArgs(2, 3),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, rest, err := a.envNameN(cmd, args, 2)
			if err != nil {
				return err
			}
			return a.Backend.Push(cmd.Context(), name, rest[0], rest[1])
		},
	}
	addNameFlag(c)
	return c
}

func (a *App) pullCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "pull [name] <remote> <local>",
		Short: "Copy a file out of an environment",
		Args:  cobra.RangeArgs(2, 3),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, rest, err := a.envNameN(cmd, args, 2)
			if err != nil {
				return err
			}
			return a.Backend.Pull(cmd.Context(), name, rest[0], rest[1])
		},
	}
	addNameFlag(c)
	return c
}

// envNameN resolves the name when exactly n trailing args are required.
func (a *App) envNameN(cmd *cobra.Command, args []string, n int) (string, []string, error) {
	if len(args) == n+1 {
		return args[0], args[1:], nil
	}
	name, _, err := a.envName(cmd, nil)
	return name, args, err
}

func (a *App) checkpointCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "checkpoint [name]",
		Aliases: []string{"cp"},
		Short:   "Save the environment's filesystem",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, _, err := a.envName(cmd, args)
			if err != nil {
				return err
			}
			comment, _ := cmd.Flags().GetString("comment")
			cp, err := a.Backend.Checkpoint(cmd.Context(), name, comment)
			if err != nil {
				return err
			}
			fmt.Fprintln(a.Out, cp.ID)
			return nil
		},
	}
	addNameFlag(c)
	c.Flags().String("comment", "", "description for the checkpoint")

	ls := &cobra.Command{
		Use:     "ls [name]",
		Aliases: []string{"list"},
		Short:   "List checkpoints",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, _, err := a.envName(cmd, args)
			if err != nil {
				return err
			}
			cps, err := a.Backend.ListCheckpoints(cmd.Context(), name)
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(a.Out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tTIME\tCOMMENT")
			for _, cp := range cps {
				fmt.Fprintf(w, "%s\t%s\t%s\n", cp.ID, cp.Time.Local().Format("2006-01-02 15:04:05"), cp.Comment)
			}
			return w.Flush()
		},
	}
	addNameFlag(ls)

	rm := &cobra.Command{
		Use:     "rm [name] <id>",
		Aliases: []string{"delete"},
		Short:   "Delete a checkpoint",
		Args:    cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, rest, err := a.envNameN(cmd, args, 1)
			if err != nil {
				return err
			}
			return a.Backend.DeleteCheckpoint(cmd.Context(), name, rest[0])
		},
	}
	addNameFlag(rm)

	c.AddCommand(ls, rm)
	return c
}

func (a *App) restoreCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "restore [name] <checkpoint-id>",
		Short: "Roll the environment back to a checkpoint (loses changes since)",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, rest, err := a.envNameN(cmd, args, 1)
			if err != nil {
				return err
			}
			if err := a.confirm(cmd, "restore "+rest[0]+" on", name); err != nil {
				return err
			}
			return a.Backend.Restore(cmd.Context(), name, rest[0])
		},
	}
	addNameFlag(c)
	c.Flags().Bool("force", false, "skip the confirmation")
	return c
}

func (a *App) startCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "start [name]",
		Short: "Start (wake) an environment",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, _, err := a.envName(cmd, args)
			if err != nil {
				return err
			}
			return a.Backend.Start(cmd.Context(), name)
		},
	}
	addNameFlag(c)
	return c
}

func (a *App) stopCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "stop [name]",
		Short: "Stop (sleep) an environment; files are kept",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, _, err := a.envName(cmd, args)
			if err != nil {
				return err
			}
			return a.Backend.Stop(cmd.Context(), name)
		},
	}
	addNameFlag(c)
	return c
}

func (a *App) destroyCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "destroy [name]",
		Short: "Delete an environment and all its checkpoints",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, _, err := a.envName(cmd, args)
			if err != nil {
				return err
			}
			if err := a.confirm(cmd, "permanently destroy", name); err != nil {
				return err
			}
			if err := a.Backend.Destroy(cmd.Context(), name); err != nil {
				return err
			}
			if a.ActiveEnv() == name {
				_ = config.SetActiveEnv("")
			}
			return nil
		},
	}
	addNameFlag(c)
	c.Flags().Bool("force", false, "skip the confirmation")
	return c
}

func (a *App) useCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "use [name]",
		Short: "Set the active environment for this directory (writes .pooka)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if unset, _ := cmd.Flags().GetBool("unset"); unset {
				return config.SetActiveEnv("")
			}
			if len(args) == 0 {
				if n := a.ActiveEnv(); n != "" {
					fmt.Fprintln(a.Out, n)
					return nil
				}
				return errors.New("no active environment; use: pooka use <name>")
			}
			if _, err := a.Backend.Get(cmd.Context(), args[0]); err != nil {
				return err
			}
			return config.SetActiveEnv(args[0])
		},
	}
	c.Flags().Bool("unset", false, "remove the .pooka file")
	return c
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
