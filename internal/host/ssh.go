package host

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
)

// SSH runs commands through the system ssh binary, so the user's keys, agent and
// ~/.ssh/config apply.
type SSH struct {
	Target string // user@host or an ssh config alias
}

// NewSSH returns a Host that connects to target.
func NewSSH(target string) *SSH { return &SSH{Target: target} }

// baseArgs enables connection multiplexing so one SSH session serves every call in a
// command. Without it each call pays a full handshake, about 0.8 s on a LAN.
func (s *SSH) baseArgs() []string {
	dir := filepath.Join(os.TempDir(), "pooka-ssh-"+strconv.Itoa(os.Getuid()))
	_ = os.MkdirAll(dir, 0o700)
	return []string{
		"-o", "BatchMode=yes",
		"-o", "ControlMaster=auto",
		"-o", "ControlPath=" + filepath.Join(dir, "%C"),
		"-o", "ControlPersist=120s",
	}
}

func (s *SSH) Run(ctx context.Context, cmd Cmd) (int, error) {
	args := s.baseArgs()
	if cmd.DedicatedSession {
		args = []string{"-o", "BatchMode=yes", "-o", "ControlMaster=no", "-o", "ControlPath=none"}
	}
	if cmd.TTY {
		args = append(args, "-t")
	}
	args = append(args, s.Target, "--", ShellJoin(cmd.Args))
	c := exec.CommandContext(ctx, "ssh", args...)
	if cmd.DedicatedSession {
		killWithParent(c)
	}
	if cmd.TTY {
		c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	} else {
		c.Stdin, c.Stdout, c.Stderr = cmd.Stdin, cmd.Stdout, cmd.Stderr
	}
	err := c.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return 0, nil
	case errors.As(err, &ee):
		// 255 may be ssh itself failing or the remote command. ssh prints its own
		// error on stderr, so pass the code through and let the caller show stderr.
		return ee.ExitCode(), nil
	default:
		return -1, fmt.Errorf("run ssh: %w", err)
	}
}

func (s *SSH) CopyTo(ctx context.Context, local, remote string) error {
	return s.scp(ctx, local, s.Target+":"+remote)
}

func (s *SSH) CopyFrom(ctx context.Context, remote, local string) error {
	return s.scp(ctx, s.Target+":"+remote, local)
}

func (s *SSH) scp(ctx context.Context, src, dst string) error {
	c := exec.CommandContext(ctx, "scp", append(append([]string{"-q"}, s.baseArgs()...), src, dst)...)
	c.Stderr = os.Stderr
	if err := c.Run(); err != nil {
		return fmt.Errorf("scp %s -> %s: %w", src, dst, err)
	}
	return nil
}
