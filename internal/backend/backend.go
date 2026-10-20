// Package backend defines the interface between pooka commands and a hypervisor.
package backend

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"time"
)

// Env is one managed environment.
type Env struct {
	Name   string
	VMID   int
	Node   string
	Status string // running, stopped
	IP     string // empty when stopped or unknown
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
}

// ExecOptions control a command inside an environment.
type ExecOptions struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	TTY    bool
	Root   bool
	Dir    string
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
