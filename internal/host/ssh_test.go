package host

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func recordingSSH(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	record := filepath.Join(dir, "args")
	t.Setenv("TMPDIR", dir)
	t.Setenv("SSH_RECORD", record)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$SSH_RECORD\"\nif [ \"$SSH_BLOCK\" = yes ]; then exec sleep 30; fi\ncat\nexit \"${SSH_EXIT:-0}\"\n"
	for _, name := range []string{"ssh", "scp"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	return record
}

func TestSSHArgumentsAndExit(t *testing.T) {
	record := recordingSSH(t)
	t.Setenv("SSH_EXIT", "7")
	var out bytes.Buffer
	code, err := NewSSH("root@test").Run(context.Background(), Cmd{Args: []string{"printf", "a'b; $HOME"}, DedicatedSession: true, Stdin: strings.NewReader("input"), Stdout: &out})
	if err != nil || code != 7 || out.String() != "input" {
		t.Fatalf("code=%d err=%v output=%q", code, err, out.String())
	}
	raw, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	want := "-o\nBatchMode=yes\n-o\nControlMaster=no\n-o\nControlPath=none\nroot@test\n--\n" + ShellJoin([]string{"printf", "a'b; $HOME"}) + "\n"
	if string(raw) != want {
		t.Fatalf("args=%q want=%q", raw, want)
	}
}

func TestSSHCancellation(t *testing.T) {
	recordingSSH(t)
	t.Setenv("SSH_BLOCK", "yes")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); NewSSH("test").Run(ctx, Cmd{Args: []string{"true"}}) }()
	select {
	case <-done:
		if ctx.Err() == nil {
			t.Fatal("SSH exited before cancellation")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("SSH survived cancellation")
	}
}

func TestSCPCopyArgumentsAndFailure(t *testing.T) {
	record := recordingSSH(t)
	for _, tc := range []struct {
		name string
		from bool
		fail bool
	}{{"push", false, false}, {"pull", true, false}, {"failure", false, true}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SSH_EXIT", "0")
			if tc.fail {
				t.Setenv("SSH_EXIT", "1")
			}
			s := NewSSH("root@test")
			var err error
			if tc.from {
				err = s.CopyFrom(context.Background(), "/remote file", "/local file")
			} else {
				err = s.CopyTo(context.Background(), "/local file", "/remote file")
			}
			if (err != nil) != tc.fail {
				t.Fatalf("error=%v", err)
			}
			raw, e := os.ReadFile(record)
			if e != nil {
				t.Fatal(e)
			}
			want := "/local file\nroot@test:/remote file\n"
			if tc.from {
				want = "root@test:/remote file\n/local file\n"
			}
			if !strings.HasSuffix(string(raw), want) || !strings.Contains(string(raw), "BatchMode=yes") {
				t.Fatalf("args=%q", raw)
			}
		})
	}
}
