package host

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestLocalCancelEndsChild(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		t.Run(fmt.Sprintf("pty=%t", terminal), func(t *testing.T) {
			testLocalCancelEndsChild(t, terminal)
		})
	}
}

func testLocalCancelEndsChild(t *testing.T, terminal bool) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pidFile := filepath.Join(t.TempDir(), "child")
	done := make(chan struct{})
	go func() {
		defer close(done)
		cmd := Cmd{Args: []string{"sh", "-c", `sleep 30 & echo $! > "$1"; wait`, "sh", pidFile}, Stdout: io.Discard, Stderr: io.Discard}
		if terminal {
			cmd.PTY = &PTY{}
		}
		Local{}.Run(ctx, cmd)
	}()
	var pid int
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		b, _ := os.ReadFile(pidFile)
		pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		if pid > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pid <= 0 {
		t.Fatal("child did not start")
	}
	defer syscall.Kill(pid, syscall.SIGKILL)
	cancel()
	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if syscall.Kill(pid, 0) != nil {
			break
		}
		// A zombie has exited but its parent has not reaped it yet.
		b, _ := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if strings.Contains(string(b), ") Z ") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if time.Now().After(deadline) {
		t.Error("child still runs one second after cancellation")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Error("Run did not return after cancellation")
	}
}

func TestLocalRunPipes(t *testing.T) {
	var out, errb bytes.Buffer
	code, err := Local{}.Run(context.Background(), Cmd{Args: []string{"sh", "-c", "cat; echo err >&2; exit 3"}, Stdin: strings.NewReader("in"), Stdout: &out, Stderr: &errb})
	if err != nil || code != 3 || out.String() != "in" || errb.String() != "err\n" {
		t.Errorf("code=%d err=%v out=%q errb=%q", code, err, out.String(), errb.String())
	}
}

func TestLocalRunPTY(t *testing.T) {
	var out bytes.Buffer
	resize := make(chan Winsize, 1)
	// The command reports its terminal size, waits for a resize, reports again.
	script := `stty size; read x; stty size`
	done := make(chan struct{})
	var code int
	var err error
	go func() {
		code, err = Local{}.Run(context.Background(), Cmd{Args: []string{"sh", "-c", script}, Stdin: &slowReader{after: 300 * time.Millisecond, data: "go\n"}, Stdout: &out, PTY: &PTY{Size: Winsize{Rows: 30, Cols: 100}, Resize: resize}})
		close(done)
	}()
	time.Sleep(100 * time.Millisecond)
	resize <- Winsize{Rows: 40, Cols: 120}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("pty command did not finish")
	}
	if err != nil || code != 0 {
		t.Fatalf("code=%d err=%v out=%q", code, err, out.String())
	}
	s := out.String()
	if !strings.Contains(s, "30 100") || !strings.Contains(s, "40 120") {
		t.Errorf("expected both sizes in output, got %q", s)
	}
}

// slowReader delivers data once after a delay, then EOF.
type slowReader struct {
	after time.Duration
	data  string
	done  bool
}

func (r *slowReader) Read(p []byte) (int, error) {
	if r.done {
		time.Sleep(50 * time.Millisecond)
		return 0, errEOF
	}
	time.Sleep(r.after)
	r.done = true
	return copy(p, r.data), nil
}

var errEOF = eof{}

type eof struct{}

func (eof) Error() string { return "EOF" }

// A stdin that never ends must not keep Run from returning once the process exits.
func TestLocalRunDoesNotWaitForStdinEOF(t *testing.T) {
	pr, _ := io.Pipe() // nothing ever writes or closes
	done := make(chan struct{})
	var code int
	var err error
	go func() {
		code, err = Local{}.Run(context.Background(), Cmd{Args: []string{"true"}, Stdin: pr, Stdout: io.Discard, Stderr: io.Discard})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run waited for stdin EOF after the process exited")
	}
	if err != nil || code != 0 {
		t.Errorf("code=%d err=%v", code, err)
	}
}
