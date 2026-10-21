package host

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"

	"github.com/creack/pty"
)

// Local runs commands on this machine. The daemon uses it on the Proxmox host.
type Local struct{}

func (Local) Run(ctx context.Context, cmd Cmd) (int, error) {
	if len(cmd.Args) == 0 {
		return -1, errors.New("empty command")
	}
	c := exec.CommandContext(ctx, cmd.Args[0], cmd.Args[1:]...)
	switch {
	case cmd.PTY != nil:
		return runPTY(c, cmd)
	case cmd.TTY:
		c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	default:
		c.Stdin, c.Stdout, c.Stderr = cmd.Stdin, cmd.Stdout, cmd.Stderr
	}
	return exitCode(c.Run())
}

func runPTY(c *exec.Cmd, cmd Cmd) (int, error) {
	size := &pty.Winsize{Rows: cmd.PTY.Size.Rows, Cols: cmd.PTY.Size.Cols}
	if size.Rows == 0 || size.Cols == 0 {
		size = &pty.Winsize{Rows: 24, Cols: 80}
	}
	f, err := pty.StartWithSize(c, size)
	if err != nil {
		return -1, fmt.Errorf("start pty: %w", err)
	}
	ptmx := &guardedPTY{f: f}
	defer ptmx.Close()

	done := make(chan struct{})
	var wg sync.WaitGroup
	if cmd.Stdin != nil {
		// Not joined: it may block in cmd.Stdin.Read after the process exits. The
		// guard makes its late writes harmless.
		go func() {
			_, _ = io.Copy(ptmx, cmd.Stdin)
			// Stdin closed: send EOT so a shell exits, then let the process decide.
			_, _ = ptmx.Write([]byte{4})
		}()
	}
	if cmd.PTY.Resize != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case ws, ok := <-cmd.PTY.Resize:
					if !ok {
						return
					}
					ptmx.Setsize(ws)
				case <-done:
					return
				}
			}
		}()
	}
	if cmd.Stdout != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = io.Copy(cmd.Stdout, f) // returns on EIO when the slave closes
		}()
	}
	err = c.Wait()
	close(done)
	wg.Wait()
	return exitCode(err)
}

// guardedPTY serialises writes, resizes and close on the pty master so a late write
// from the stdin goroutine cannot race with Close.
type guardedPTY struct {
	mu     sync.Mutex
	f      *os.File
	closed bool
}

func (g *guardedPTY) Write(p []byte) (int, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return 0, os.ErrClosed
	}
	return g.f.Write(p)
}

func (g *guardedPTY) Setsize(ws Winsize) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.closed {
		_ = pty.Setsize(g.f, &pty.Winsize{Rows: ws.Rows, Cols: ws.Cols})
	}
}

func (g *guardedPTY) Close() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return nil
	}
	g.closed = true
	return g.f.Close()
}

func exitCode(err error) (int, error) {
	var ee *exec.ExitError
	switch {
	case err == nil:
		return 0, nil
	case errors.As(err, &ee):
		return ee.ExitCode(), nil
	default:
		return -1, err
	}
}

func (Local) CopyTo(_ context.Context, local, remote string) error {
	return copyFile(local, remote)
}

func (Local) CopyFrom(_ context.Context, remote, local string) error {
	return copyFile(remote, local)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
