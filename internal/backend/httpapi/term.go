package httpapi

import (
	"context"
	"io"
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/term"
)

// terminal is what TTY exec needs from the local terminal. Tests supply a fake.
type terminal interface {
	in() io.Reader
	out() io.Writer
	size() (rows, cols uint16)
	// makeRaw switches the terminal to raw mode and returns the restore function.
	makeRaw() (func(), error)
	// resizes delivers one value per window size change until ctx ends.
	resizes(ctx context.Context) <-chan struct{}
}

// stdTerminal is the process's own stdin and stdout.
type stdTerminal struct{}

func (stdTerminal) in() io.Reader  { return os.Stdin }
func (stdTerminal) out() io.Writer { return os.Stdout }

func (stdTerminal) size() (uint16, uint16) {
	cols, rows, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || rows <= 0 || cols <= 0 {
		return 24, 80
	}
	return uint16(rows), uint16(cols)
}

func (stdTerminal) makeRaw() (func(), error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return func() {}, nil
	}
	old, err := term.MakeRaw(fd)
	if err != nil {
		return nil, err
	}
	return func() { _ = term.Restore(fd, old) }, nil
}

func (stdTerminal) resizes(ctx context.Context) <-chan struct{} {
	out := make(chan struct{}, 1)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGWINCH)
	go func() {
		defer signal.Stop(sig)
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case <-sig:
				select {
				case out <- struct{}{}:
				default:
				}
			}
		}
	}()
	return out
}
