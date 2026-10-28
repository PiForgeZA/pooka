package lxc

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/PiForgeZA/pooka/internal/backend"
	"github.com/PiForgeZA/pooka/internal/host"
)

func TestMutationsRejectUnmanagedContainer(t *testing.T) {
	ctx := context.Background()
	for name, run := range map[string]func(*Backend) error{
		"start":             func(b *Backend) error { return b.Start(ctx, "other") },
		"stop":              func(b *Backend) error { return b.Stop(ctx, "other") },
		"destroy":           func(b *Backend) error { return b.Destroy(ctx, "other") },
		"restore":           func(b *Backend) error { return b.Restore(ctx, "other", "cp-1") },
		"checkpoint":        func(b *Backend) error { _, e := b.Checkpoint(ctx, "other", ""); return e },
		"delete checkpoint": func(b *Backend) error { return b.DeleteCheckpoint(ctx, "other", "cp-1") },
		"push":              func(b *Backend) error { return b.Push(ctx, "other", "local", "remote") },
		"pull":              func(b *Backend) error { return b.Pull(ctx, "other", "remote", "local") },
	} {
		t.Run(name, func(t *testing.T) {
			f := newFake()
			f.Strict = true
			if err := run(newBackend(f)); !errors.Is(err, backend.ErrNotFound) {
				t.Fatalf("error=%v", err)
			}
			for _, c := range f.Calls {
				if c != "pct list" && c != "sh -c "+confScript {
					t.Errorf("unexpected command: %s", c)
				}
			}
			if len(f.Copies) != 0 {
				t.Fatal(f.Copies)
			}
		})
	}
}

func TestRestoreStopsAtFailedPrerequisite(t *testing.T) {
	steps := []string{"pct stop 9100", "pct rollback 9100 cp-1", "pct config 9100", "pvesh get /storage/local-lvm --output-format json", "pvesm path local-lvm:vm-9100-disk-0", "tune2fs -f -E clear_mmp /dev/pve/vm-9100-disk-0", "pct start 9100"}
	for i, step := range steps {
		t.Run(step, func(t *testing.T) {
			f := newFake()
			f.Strict = true
			f.Rules = append([]host.FakeRule{{Prefix: step, Exit: 1, Stderr: "injected failure"}}, f.Rules...)
			for _, s := range steps {
				f.On(s, "")
			}
			err := newBackend(f).Restore(context.Background(), "dev1", "cp-1")
			if err == nil || !strings.Contains(err.Error(), "injected failure") {
				t.Fatalf("error=%v", err)
			}
			var got []string
			for _, c := range f.Calls {
				for _, s := range steps {
					if c == s {
						got = append(got, c)
					}
				}
			}
			if !reflect.DeepEqual(got, steps[:i+1]) {
				t.Fatalf("calls=%v want=%v", got, steps[:i+1])
			}
		})
	}
}

func TestCheckpointCommandsAndErrors(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[fail], func(t *testing.T) {
			f := newFake()
			f.Strict = true
			if fail {
				f.OnExit("pct snapshot", 1, "snapshot failure")
				f.OnExit("pct delsnapshot", 1, "delete failure")
			} else {
				f.On("pct snapshot", "")
				f.On("pct delsnapshot", "")
			}
			b := newBackend(f)
			cp, err := b.Checkpoint(context.Background(), "dev1", "before update")
			if (err != nil) != fail {
				t.Fatalf("checkpoint: %v", err)
			}
			if !fail && (!strings.HasPrefix(cp.ID, "cp-") || cp.Comment != "before update" || !f.Called("pct snapshot 9100 "+cp.ID+" --description before update")) {
				t.Fatalf("checkpoint=%+v calls=%v", cp, f.Calls)
			}
			err = b.DeleteCheckpoint(context.Background(), "dev1", "cp-1")
			if (err != nil) != fail || !f.Called("pct delsnapshot 9100 cp-1") {
				t.Fatalf("delete=%v calls=%v", err, f.Calls)
			}
		})
	}
}

func TestDestroyStopsAtFailure(t *testing.T) {
	for _, step := range []string{"pct stop 9100", "pct destroy 9100"} {
		t.Run(step, func(t *testing.T) {
			f := newFake()
			f.Strict = true
			f.On("pct stop", "")
			f.On("pct destroy", "")
			f.Rules = append([]host.FakeRule{{Prefix: step, Exit: 1, Stderr: "injected"}}, f.Rules...)
			if err := newBackend(f).Destroy(context.Background(), "dev1"); err == nil {
				t.Fatal("lost failure")
			}
			if step == "pct stop 9100" && f.Called("pct destroy") {
				t.Fatal("purged after stop failed")
			}
		})
	}
}
