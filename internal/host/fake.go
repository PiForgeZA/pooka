package host

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
)

// Fake is a scripted Host for unit tests. Match a command by prefix of its joined
// arguments. The first matching rule wins. Unmatched commands return exit 0 with no
// output, unless Strict is set.
type Fake struct {
	mu     sync.Mutex
	Rules  []FakeRule
	Calls  []string // joined args of every Run call, in order
	Copies []string // "to local->remote" or "from remote->local"
	Strict bool
}

// FakeRule maps a command prefix to a response.
type FakeRule struct {
	Prefix string
	Stdout string
	Stderr string
	Exit   int
	// Fn, if set, computes the response from the call. It runs after the static fields.
	Fn func(call string, n int) (stdout string, exit int)
}

func (f *Fake) On(prefix, stdout string) *Fake {
	f.Rules = append(f.Rules, FakeRule{Prefix: prefix, Stdout: stdout})
	return f
}

func (f *Fake) OnExit(prefix string, exit int, stderr string) *Fake {
	f.Rules = append(f.Rules, FakeRule{Prefix: prefix, Exit: exit, Stderr: stderr})
	return f
}

func (f *Fake) Run(_ context.Context, cmd Cmd) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	call := strings.Join(cmd.Args, " ")
	f.Calls = append(f.Calls, call)
	n := 0
	for _, c := range f.Calls {
		if c == call {
			n++
		}
	}
	for _, r := range f.Rules {
		if strings.HasPrefix(call, r.Prefix) {
			out, exit := r.Stdout, r.Exit
			if r.Fn != nil {
				out, exit = r.Fn(call, n)
			}
			if cmd.Stdout != nil {
				io.WriteString(cmd.Stdout, out)
			}
			if cmd.Stderr != nil {
				io.WriteString(cmd.Stderr, r.Stderr)
			}
			return exit, nil
		}
	}
	if f.Strict {
		return -1, fmt.Errorf("fake host: no rule for %q", call)
	}
	return 0, nil
}

func (f *Fake) CopyTo(_ context.Context, local, remote string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Copies = append(f.Copies, "to "+local+"->"+remote)
	return nil
}

func (f *Fake) CopyFrom(_ context.Context, remote, local string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Copies = append(f.Copies, "from "+remote+"->"+local)
	return nil
}

// Called reports whether any call starts with prefix.
func (f *Fake) Called(prefix string) bool {
	for _, c := range f.Calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}
