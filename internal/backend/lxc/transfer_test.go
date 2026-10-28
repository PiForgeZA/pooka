package lxc

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PiForgeZA/pooka/internal/host"
)

type copyHost struct {
	*host.Fake
	copyErr error
}

func (h copyHost) CopyTo(ctx context.Context, a, b string) error {
	h.Fake.CopyTo(ctx, a, b)
	return h.copyErr
}
func (h copyHost) CopyFrom(ctx context.Context, a, b string) error {
	h.Fake.CopyFrom(ctx, a, b)
	return h.copyErr
}

func TestTransferCleanupAndFailure(t *testing.T) {
	for _, direction := range []string{"push", "pull"} {
		for _, failure := range []string{"none", "copy", "pct"} {
			t.Run(direction+"/"+failure, func(t *testing.T) {
				f := newFake()
				f.Strict = true
				f.On("rm -f /tmp/pooka-", "")
				if failure == "pct" {
					f.OnExit("pct "+direction, 1, "transfer failed")
				} else {
					f.On("pct "+direction, "")
				}
				h := copyHost{Fake: f}
				if failure == "copy" {
					h.copyErr = errors.New("copy failed")
				}
				b := New(h, Options{})
				local := filepath.Join(t.TempDir(), "local file")
				if err := os.WriteFile(local, []byte("payload"), 0600); err != nil {
					t.Fatal(err)
				}
				var err error
				if direction == "push" {
					err = b.Push(context.Background(), "dev1", local, "/remote file")
				} else {
					err = b.Pull(context.Background(), "dev1", "/remote file", local)
				}
				if (err != nil) != (failure != "none") {
					t.Fatalf("error=%v", err)
				}
				cleanup := !(direction == "push" && failure == "copy" || direction == "pull" && failure == "pct")
				if f.Called("rm -f") != cleanup {
					t.Fatalf("cleanup=%v calls=%v", cleanup, f.Calls)
				}
				if direction == "push" && failure == "copy" && f.Called("pct push") {
					t.Fatal("push after failed copy")
				}
				if direction == "pull" && failure == "pct" && len(f.Copies) != 0 {
					t.Fatal("copy after failed pull")
				}
				if direction == "push" && failure != "copy" {
					found := false
					for _, c := range f.Calls {
						if strings.HasPrefix(c, "pct push 9100 /tmp/pooka-") && strings.HasSuffix(c, " /remote file --user pooka --group pooka") {
							found = true
						}
					}
					if !found {
						t.Fatalf("missing ownership/path: %v", f.Calls)
					}
				}
			})
		}
	}
}
