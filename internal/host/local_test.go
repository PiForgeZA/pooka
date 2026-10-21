package host

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"
)

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
