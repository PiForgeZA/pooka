package daemon

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/PiForgeZA/pooka/internal/backend"
)

type execOverride struct {
	backend.Backend
	run func(context.Context, string, []string, backend.ExecOptions) (int, error)
}

func (b execOverride) Exec(ctx context.Context, n string, c []string, o backend.ExecOptions) (int, error) {
	return b.run(ctx, n, c, o)
}

func TestIdleProbeFailureDoesNotStop(t *testing.T) {
	for _, code := range []int{-1, 1} {
		s, fb, now := newIdleServer(t)
		*now = now.Add(time.Hour)
		s.Backend = execOverride{Backend: fb, run: func(context.Context, string, []string, backend.ExecOptions) (int, error) {
			if code < 0 {
				return -1, errors.New("transport failed")
			}
			return code, nil
		}}
		if got := s.idleTick(context.Background()); len(got) != 0 || len(stops(fb)) != 0 {
			t.Fatalf("stopped after probe failed: %v", fb.Calls)
		}
	}
}

func TestIdleLoopCancellation(t *testing.T) {
	s, _, _ := newIdleServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.RunIdle(ctx, time.Hour); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("idle loop did not stop")
	}
}

func TestIdleRechecksActivityAfterProbe(t *testing.T) {
	for _, kind := range []string{"request", "exec"} {
		t.Run(kind, func(t *testing.T) {
			s, fb, now := newIdleServer(t)
			*now = now.Add(time.Hour)
			entered, release := make(chan struct{}), make(chan struct{})
			s.Backend = execOverride{Backend: fb, run: func(context.Context, string, []string, backend.ExecOptions) (int, error) {
				close(entered)
				<-release
				return 0, nil
			}}
			done := make(chan []string, 1)
			go func() { done <- s.idleTick(context.Background()) }()
			select {
			case <-entered:
			case <-time.After(time.Second):
				close(release)
				t.Fatal("probe did not start")
			}
			if kind == "request" {
				s.Touch("dev1")
			} else {
				s.addSession("dev1", execParams{}, func() {})
			}
			close(release)
			select {
			case got := <-done:
				if len(got) != 0 || len(stops(fb)) != 0 {
					t.Fatalf("stale decision stopped active environment: %v", fb.Calls)
				}
			case <-time.After(time.Second):
				t.Fatal("probe did not finish")
			}
		})
	}
}
