// Package host runs commands on the Proxmox host. Phase 1 uses SSH.
// Commands and backends must not depend on the transport.
package host

import (
	"context"
	"io"
	"strings"
)

// Cmd is one command to run on the host.
type Cmd struct {
	Args   []string
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	// TTY attaches the local terminal. Stdin, Stdout and Stderr are ignored.
	TTY bool
}

// Host runs commands on the Proxmox host and copies files to and from it.
type Host interface {
	// Run executes the command and returns its exit code. A non-zero exit code is not
	// an error. An error means the command could not run at all.
	Run(ctx context.Context, cmd Cmd) (int, error)
	// CopyTo copies a local file to a path on the host.
	CopyTo(ctx context.Context, local, remote string) error
	// CopyFrom copies a file on the host to a local path.
	CopyFrom(ctx context.Context, remote, local string) error
}

// ShellQuote quotes one argument for a POSIX shell.
func ShellQuote(s string) string {
	if s == "" {
		return "''"
	}
	safe := true
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
			strings.ContainsRune("-_./=:@%+,", r)) {
			safe = false
			break
		}
	}
	if safe {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// ShellJoin quotes and joins arguments for a POSIX shell.
func ShellJoin(args []string) string {
	parts := make([]string, len(args))
	for i, a := range args {
		parts[i] = ShellQuote(a)
	}
	return strings.Join(parts, " ")
}
