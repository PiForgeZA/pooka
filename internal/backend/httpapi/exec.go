package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"sync"

	"github.com/coder/websocket"

	"github.com/PiForgeZA/pooka/internal/backend"
)

// Frame prefixes of the Sprites WebSocket exec protocol. See docs/sprites-api.
const (
	frameStdin    byte = 0x00
	frameStdout   byte = 0x01
	frameStderr   byte = 0x02
	frameExit     byte = 0x03
	frameStdinEOF byte = 0x04
)

// Exec runs cmd over the Sprites WebSocket exec protocol. With TTY it puts the local
// terminal in raw mode, forwards window size changes, and restores it on return.
// Root uses the pooka extension route with root=true: the Sprites API has no
// run-as-root option, and sudo would start a new session without the terminal.
func (c *Client) Exec(ctx context.Context, name string, cmd []string, o backend.ExecOptions) (int, error) {
	if len(cmd) == 0 {
		return -1, errors.New("exec: no command")
	}
	if o.PTY != nil {
		return -1, errors.New("exec: PTY mode is for the daemon, not the HTTP client")
	}
	stdin, stdout, stderr := o.Stdin, o.Stdout, o.Stderr
	q := url.Values{}
	execPath := spritePath(name, "exec")
	if o.Root {
		execPath = pookaPath(name, "exec")
		q.Set("root", "true")
	}
	for _, a := range cmd {
		q.Add("cmd", a)
	}
	q.Set("path", cmd[0])
	var t terminal
	if o.TTY {
		t = c.term
		if t == nil {
			t = stdTerminal{}
		}
		stdin, stdout, stderr = t.in(), t.out(), t.out()
		rows, cols := t.size()
		q.Set("tty", "true")
		q.Set("rows", strconv.Itoa(int(rows)))
		q.Set("cols", strconv.Itoa(int(cols)))
		if term := os.Getenv("TERM"); term != "" {
			q.Add("env", "TERM="+term)
		}
	}
	for _, e := range o.Env {
		q.Add("env", e)
	}
	if o.Dir != "" {
		q.Set("dir", o.Dir)
	}
	q.Set("stdin", strconv.FormatBool(stdin != nil))
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}

	wsURL := c.URL + execPath + "?" + q.Encode()
	conn, resp, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPClient: c.HTTP, HTTPHeader: c.headers()})
	if c.Debug != nil {
		status := "error"
		if resp != nil {
			status = strconv.Itoa(resp.StatusCode)
		}
		fmt.Fprintf(c.Debug, "[daemon ws %s] exec %s\n", status, cmd[0])
	}
	if err != nil {
		if resp != nil && resp.StatusCode >= 400 {
			return -1, c.statusError(resp)
		}
		return -1, c.reachError(err)
	}
	defer conn.CloseNow()
	conn.SetReadLimit(16 << 20)

	if t != nil {
		restore, err := t.makeRaw()
		if err != nil {
			return -1, err
		}
		defer restore()
	}

	// Every writer on the socket goes through one mutex.
	var wmu sync.Mutex
	write := func(typ websocket.MessageType, b []byte) error {
		wmu.Lock()
		defer wmu.Unlock()
		return conn.Write(ctx, typ, b)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	if stdin != nil {
		go pumpStdin(ctx, stdin, write)
	}
	if t != nil {
		go func() {
			for range t.resizes(ctx) {
				rows, cols := t.size()
				b, _ := json.Marshal(map[string]any{"type": "resize", "rows": rows, "cols": cols})
				_ = write(websocket.MessageText, b)
			}
		}()
	}

	code := -1
	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			if code >= 0 {
				return code, nil
			}
			if ctx.Err() != nil {
				return -1, ctx.Err()
			}
			return -1, fmt.Errorf("connection to pookad closed before the command finished: %w", err)
		}
		switch typ {
		case websocket.MessageBinary:
			if len(data) == 0 {
				continue
			}
			switch data[0] {
			case frameStdout:
				_, _ = stdout.Write(data[1:])
			case frameStderr:
				_, _ = stderr.Write(data[1:])
			case frameExit:
				if len(data) > 1 {
					code = int(data[1])
				}
			}
		case websocket.MessageText:
			var msg struct {
				Type     string `json:"type"`
				ExitCode int    `json:"exit_code"`
			}
			if json.Unmarshal(data, &msg) == nil && msg.Type == "exit" {
				if code < 0 {
					code = msg.ExitCode
				}
				return code, nil
			}
		}
	}
}

// pumpStdin forwards stdin as frames and sends the EOF frame when the reader ends.
// A read on a terminal cannot be interrupted; the goroutine then ends with the
// process, after the command has already finished.
func pumpStdin(ctx context.Context, r io.Reader, write func(websocket.MessageType, []byte) error) {
	buf := make([]byte, 32<<10)
	for {
		n, err := r.Read(buf[1:])
		if n > 0 {
			buf[0] = frameStdin
			if werr := write(websocket.MessageBinary, buf[:1+n]); werr != nil {
				return
			}
		}
		if err != nil || ctx.Err() != nil {
			_ = write(websocket.MessageBinary, []byte{frameStdinEOF})
			return
		}
	}
}
