package host

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"
)

// Logged wraps a Host and writes each call with its duration to Out.
// Enabled by POOKA_DEBUG=1.
type Logged struct {
	Host
	Out io.Writer
}

func (l Logged) Run(ctx context.Context, cmd Cmd) (int, error) {
	start := time.Now()
	code, err := l.Host.Run(ctx, cmd)
	fmt.Fprintf(l.Out, "[host %6.2fs exit %d] %s\n", time.Since(start).Seconds(), code, truncate(strings.Join(cmd.Args, " "), 120))
	return code, err
}

func (l Logged) CopyTo(ctx context.Context, local, remote string) error {
	start := time.Now()
	err := l.Host.CopyTo(ctx, local, remote)
	fmt.Fprintf(l.Out, "[host %6.2fs] copy to %s\n", time.Since(start).Seconds(), remote)
	return err
}

func (l Logged) CopyFrom(ctx context.Context, remote, local string) error {
	start := time.Now()
	err := l.Host.CopyFrom(ctx, remote, local)
	fmt.Fprintf(l.Out, "[host %6.2fs] copy from %s\n", time.Since(start).Seconds(), remote)
	return err
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
