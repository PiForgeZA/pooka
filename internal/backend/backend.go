// Package backend defines the interface between pooka commands and a hypervisor.
package backend

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"time"

	"github.com/PiForgeZA/pooka/internal/host"
)

// Env is one managed environment.
type Env struct {
	Name    string
	VMID    int
	Node    string
	Status  string // running, stopped
	IP      string // empty when stopped or unknown
	URL     string // https://<name>-<org>.<domain>; empty when no proxy serves it
	Created time.Time
	Settings
}

// Settings are the per-environment fields that live in the container description
// (ADR 0008, 0009). In an Update, an empty or nil field keeps the current value.
type Settings struct {
	URLAuth     string         // "sprite" (token holders only) or "public"; "" means sprite
	IdleTimeout *time.Duration // nil means the daemon default; 0 means never stop
}

// ValidateURLAuth checks an auth value. "" is allowed and means "keep".
func ValidateURLAuth(v string) error {
	if v == "" || v == "sprite" || v == "public" {
		return nil
	}
	return fmt.Errorf("invalid url auth %q: use sprite or public", v)
}

// Checkpoint is one saved filesystem state.
type Checkpoint struct {
	ID      string
	Time    time.Time
	Comment string
}

// CreateOptions control a new environment.
type CreateOptions struct {
	Cores     int
	MemoryMB  int
	DiskGB    int
	SSHPubKey string // contents of the public key, may be empty
	// IdleTimeout, when set, becomes the environment's own idle timeout. nil
	// leaves the daemon default in force.
	IdleTimeout *time.Duration
}

// ExecOptions control a command inside an environment.
type ExecOptions struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	TTY    bool      // attach the local terminal (CLI use)
	PTY    *host.PTY // run under a pseudo-terminal driven by Stdin and Stdout (daemon use)
	Root   bool
	Dir    string
	Env    []string // KEY=VALUE, exported before the command runs
}

// Backend manages environments on one hypervisor type.
type Backend interface {
	Create(ctx context.Context, name string, opts CreateOptions) (Env, error)
	List(ctx context.Context) ([]Env, error)
	Get(ctx context.Context, name string) (Env, error)
	Start(ctx context.Context, name string) error
	Stop(ctx context.Context, name string) error
	Destroy(ctx context.Context, name string) error
	Exec(ctx context.Context, name string, cmd []string, opts ExecOptions) (int, error)
	Push(ctx context.Context, name, local, remote string) error
	Pull(ctx context.Context, name, remote, local string) error
	Checkpoint(ctx context.Context, name, comment string) (Checkpoint, error)
	ListCheckpoints(ctx context.Context, name string) ([]Checkpoint, error)
	DeleteCheckpoint(ctx context.Context, name, id string) error
	Restore(ctx context.Context, name, id string) error
	// Update writes the given settings. Empty or nil fields keep their value.
	Update(ctx context.Context, name string, s Settings) error
}

// ErrNotFound means no environment has the given name.
var ErrNotFound = errors.New("environment not found")

// ErrExists means an environment with the name exists.
var ErrExists = errors.New("environment already exists")

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// ValidateName enforces SPEC section 9 step 1.
func ValidateName(name string) error {
	if !nameRe.MatchString(name) {
		return fmt.Errorf("invalid name %q: use lowercase letters, digits and hyphens, max 32 characters", name)
	}
	return nil
}
