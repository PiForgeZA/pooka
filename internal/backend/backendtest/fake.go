// Package backendtest holds an in-memory Backend for unit tests. The daemon tests
// serve it; the httpapi tests drive the daemon that serves it. No host is needed.
package backendtest

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/PiForgeZA/pooka/internal/backend"
)

// Fake is an in-memory Backend. Exec echoes stdin, prints its args and env, and
// returns 7 for the command "exit". Every mutating call lands in Calls.
type Fake struct {
	Envs    map[string]backend.Env
	Cps     map[string][]backend.Checkpoint
	Calls   []string
	Created backend.CreateOptions // options of the last Create
	Root    []bool                // Root flag of each Exec, parallel to its Calls entry
	Pushed  map[string]string     // remote path -> contents written by Exec "cat > f"
}

// New returns a Fake with dev1 running, dev2 stopped and one checkpoint on dev1.
func New() *Fake {
	return &Fake{
		Envs: map[string]backend.Env{
			"dev1": {Name: "dev1", VMID: 9100, Status: "running", IP: "10.0.0.5", Created: time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)},
			"dev2": {Name: "dev2", VMID: 9101, Status: "stopped"},
		},
		Cps:    map[string][]backend.Checkpoint{"dev1": {{ID: "cp-1", Time: time.Unix(1700000000, 0), Comment: "first"}}},
		Pushed: map[string]string{},
	}
}

func (f *Fake) Create(_ context.Context, name string, o backend.CreateOptions) (backend.Env, error) {
	f.Created = o
	if err := backend.ValidateName(name); err != nil {
		return backend.Env{}, err
	}
	if _, ok := f.Envs[name]; ok {
		return backend.Env{}, backend.ErrExists
	}
	e := backend.Env{Name: name, VMID: 9102, Status: "running", IP: "10.0.0.7", Created: time.Now()}
	f.Envs[name] = e
	f.Calls = append(f.Calls, "create "+name)
	return e, nil
}

func (f *Fake) List(context.Context) ([]backend.Env, error) {
	out := []backend.Env{}
	for _, n := range []string{"dev1", "dev2", "dev3"} {
		if e, ok := f.Envs[n]; ok {
			out = append(out, e)
		}
	}
	return out, nil
}

func (f *Fake) Get(_ context.Context, name string) (backend.Env, error) {
	e, ok := f.Envs[name]
	if !ok {
		return backend.Env{}, backend.ErrNotFound
	}
	return e, nil
}

func (f *Fake) Start(_ context.Context, name string) error {
	if _, ok := f.Envs[name]; !ok {
		return backend.ErrNotFound
	}
	f.Calls = append(f.Calls, "start "+name)
	return nil
}

func (f *Fake) Stop(_ context.Context, name string) error {
	if _, ok := f.Envs[name]; !ok {
		return backend.ErrNotFound
	}
	f.Calls = append(f.Calls, "stop "+name)
	return nil
}

func (f *Fake) Destroy(_ context.Context, name string) error {
	if _, ok := f.Envs[name]; !ok {
		return backend.ErrNotFound
	}
	delete(f.Envs, name)
	f.Calls = append(f.Calls, "destroy "+name)
	return nil
}

func (f *Fake) Exec(_ context.Context, name string, cmd []string, o backend.ExecOptions) (int, error) {
	if _, ok := f.Envs[name]; !ok {
		return -1, backend.ErrNotFound
	}
	f.Calls = append(f.Calls, strings.Join(cmd, " "))
	f.Root = append(f.Root, o.Root)
	if cmd[0] == "exit" {
		return 7, nil
	}
	var in []byte
	if o.Stdin != nil {
		in, _ = io.ReadAll(o.Stdin)
	}
	// The daemon's fs/write runs `sh -c 'f="$1"; ...' sh <path> <mode>`. Keep the
	// contents so a test can check what a push wrote, and answer with the size.
	if cmd[0] == "sh" && len(cmd) >= 5 && strings.Contains(cmd[2], `cat > "$f"`) {
		f.Pushed[cmd[4]] = string(in)
		fmt.Fprintf(o.Stdout, "%d\n", len(in))
		return 0, nil
	}
	// fs/read is `cat -- <path>`. Serve pushed contents, else fail like cat.
	if cmd[0] == "cat" && len(cmd) == 3 {
		c, ok := f.Pushed[cmd[2]]
		if !ok {
			fmt.Fprintf(o.Stderr, "cat: %s: No such file or directory\n", cmd[2])
			return 1, nil
		}
		io.WriteString(o.Stdout, c)
		return 0, nil
	}
	if len(in) > 0 {
		io.WriteString(o.Stdout, "stdin:"+string(in)+"\n")
	}
	io.WriteString(o.Stdout, "cmd:"+strings.Join(cmd, " ")+"\n")
	if len(o.Env) > 0 {
		io.WriteString(o.Stdout, "env:"+strings.Join(o.Env, ",")+"\n")
	}
	if o.Dir != "" {
		io.WriteString(o.Stdout, "dir:"+o.Dir+"\n")
	}
	if o.Stderr != nil {
		io.WriteString(o.Stderr, "err\n")
	}
	return 0, nil
}

func (f *Fake) Push(_ context.Context, name, local, remote string) error {
	f.Calls = append(f.Calls, "push "+name+" "+local+" "+remote)
	return nil
}

func (f *Fake) Pull(_ context.Context, name, remote, local string) error {
	f.Calls = append(f.Calls, "pull "+name+" "+remote+" "+local)
	return nil
}

func (f *Fake) Checkpoint(_ context.Context, name, comment string) (backend.Checkpoint, error) {
	if _, ok := f.Envs[name]; !ok {
		return backend.Checkpoint{}, backend.ErrNotFound
	}
	cp := backend.Checkpoint{ID: fmt.Sprintf("cp-%d", len(f.Cps[name])+1), Time: time.Now(), Comment: comment}
	f.Cps[name] = append(f.Cps[name], cp)
	f.Calls = append(f.Calls, "checkpoint "+name)
	return cp, nil
}

// ListCheckpoints returns oldest first, as the LXC backend does.
func (f *Fake) ListCheckpoints(_ context.Context, name string) ([]backend.Checkpoint, error) {
	if _, ok := f.Envs[name]; !ok {
		return nil, backend.ErrNotFound
	}
	return f.Cps[name], nil
}

func (f *Fake) DeleteCheckpoint(_ context.Context, name, id string) error {
	cps := f.Cps[name]
	for i, cp := range cps {
		if cp.ID == id {
			f.Cps[name] = append(cps[:i:i], cps[i+1:]...)
			f.Calls = append(f.Calls, "delcp "+name+" "+id)
			return nil
		}
	}
	return fmt.Errorf("checkpoint %s: %w", id, backend.ErrNotFound)
}

func (f *Fake) Restore(_ context.Context, name, id string) error {
	if _, ok := f.Envs[name]; !ok {
		return backend.ErrNotFound
	}
	f.Calls = append(f.Calls, "restore "+name+" "+id)
	return nil
}
