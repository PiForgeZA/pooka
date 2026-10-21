package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/PiForgeZA/pooka/internal/backend"
	"github.com/PiForgeZA/pooka/internal/host"
)

// Frame prefixes of the Sprites WebSocket exec protocol.
const (
	frameStdin    byte = 0x00
	frameStdout   byte = 0x01
	frameStderr   byte = 0x02
	frameExit     byte = 0x03
	frameStdinEOF byte = 0x04
)

// consoleCmd is what the official CLI runs for `sprite console`. pooka maps it to a
// login shell.
const consoleCmd = "/.sprite/bin/sprite-console"

const maxPostBody = 10 << 20

// session is one running exec, kept for listing and killing.
type session struct {
	ID       string
	Env      string
	Command  string
	Workdir  string
	TTY      bool
	Created  time.Time
	cancel   context.CancelFunc
	active   bool
	lastSeen time.Time
}

type execParams struct {
	cmd   []string
	stdin bool
	tty   bool
	env   []string
	dir   string
	size  host.Winsize
}

func parseExecParams(r *http.Request) (execParams, error) {
	q := r.URL.Query()
	p := execParams{cmd: q["cmd"], env: q["env"], dir: q.Get("dir"), size: host.Winsize{Rows: 24, Cols: 80}}
	if rows, err := strconv.Atoi(q.Get("rows")); err == nil && rows > 0 && rows < 65536 {
		p.size.Rows = uint16(rows)
	}
	if cols, err := strconv.Atoi(q.Get("cols")); err == nil && cols > 0 && cols < 65536 {
		p.size.Cols = uint16(cols)
	}
	if len(p.cmd) == 0 {
		if path := q.Get("path"); path != "" {
			p.cmd = []string{path}
		} else {
			return p, errors.New("cmd query parameter is required")
		}
	}
	if p.cmd[0] == consoleCmd {
		p.cmd = []string{"bash", "-l"}
	}
	p.stdin = q.Get("stdin") == "true"
	p.tty = q.Get("tty") == "true"
	return p, nil
}

func (s *Server) addSession(name string, p execParams, cancel context.CancelFunc) *session {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	ss := &session{ID: strconv.Itoa(s.nextID), Env: name, Command: strings.Join(p.cmd, " "), Workdir: p.dir, TTY: p.tty, Created: time.Now(), cancel: cancel, active: true, lastSeen: time.Now()}
	s.sessions[ss.ID] = ss
	return ss
}

func (s *Server) endSession(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, id)
}

func (s *Server) dropSessions(env string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, ss := range s.sessions {
		if ss.Env == env {
			ss.cancel()
			delete(s.sessions, id)
		}
	}
}

// execGet serves the WebSocket exec when the client asks for an upgrade, and the
// JSON session list otherwise. Both live on the same path in the Sprites API.
func (s *Server) execGet(w http.ResponseWriter, r *http.Request) {
	if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		s.execWS(w, r)
		return
	}
	name := r.PathValue("name")
	s.mu.Lock()
	type sessionJSON struct {
		ID             string  `json:"id"`
		Created        string  `json:"created"`
		Command        string  `json:"command"`
		Workdir        string  `json:"workdir"`
		TTY            bool    `json:"tty"`
		BytesPerSecond float64 `json:"bytes_per_second"`
		IsActive       bool    `json:"is_active"`
		LastActivity   *string `json:"last_activity"`
	}
	out := []sessionJSON{}
	for _, ss := range s.sessions {
		if ss.Env != name {
			continue
		}
		last := ss.lastSeen.UTC().Format(time.RFC3339)
		out = append(out, sessionJSON{ID: ss.ID, Created: ss.Created.UTC().Format(time.RFC3339), Command: ss.Command, Workdir: ss.Workdir, TTY: ss.TTY, IsActive: ss.active, LastActivity: &last})
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"sessions": out, "count": len(out)})
}

func (s *Server) killSession(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	ss, ok := s.sessions[r.PathValue("id")]
	s.mu.Unlock()
	if !ok || ss.Env != r.PathValue("name") {
		writeError(w, http.StatusNotFound, errNoSession.Error())
		return
	}
	ss.cancel()
	st := newStreamer(w)
	st.send(streamEvent{Type: "killed", Data: "session " + ss.ID + " killed"})
}

// execPost runs a command without a TTY. The response body carries the same
// one-byte-prefixed frames as the WebSocket protocol, one frame per HTTP chunk,
// ending with an exit frame. That is what the official CLI's --http-post expects.
func (s *Server) execPost(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	p, err := parseExecParams(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := s.Backend.Get(r.Context(), name); err != nil {
		writeBackendError(w, err)
		return
	}
	var stdin io.Reader
	if p.stdin {
		stdin = http.MaxBytesReader(w, r.Body, maxPostBody)
	}
	ctx, cancel := context.WithCancel(r.Context())
	ss := s.addSession(name, p, cancel)
	defer s.endSession(ss.ID)

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	fw := &frameWriter{w: w}
	code, err := s.Backend.Exec(ctx, name, p.cmd, backend.ExecOptions{Stdin: stdin, Stdout: fw.stream(frameStdout), Stderr: fw.stream(frameStderr), Env: p.env, Dir: p.dir})
	if err != nil {
		fw.write(frameStderr, []byte(err.Error()+"\n"))
		code = 1
	}
	if code < 0 || code > 255 {
		code = 255
	}
	fw.write(frameExit, []byte{byte(code)})
}

// frameWriter writes prefixed frames to an HTTP response, flushing each one so it
// travels as its own chunk.
type frameWriter struct {
	mu sync.Mutex
	w  http.ResponseWriter
}

func (fw *frameWriter) write(kind byte, p []byte) {
	fw.mu.Lock()
	defer fw.mu.Unlock()
	buf := make([]byte, 1+len(p))
	buf[0] = kind
	copy(buf[1:], p)
	_, _ = fw.w.Write(buf)
	if f, ok := fw.w.(http.Flusher); ok {
		f.Flush()
	}
}

func (fw *frameWriter) stream(kind byte) io.Writer {
	return writerFunc(func(p []byte) (int, error) {
		fw.write(kind, p)
		return len(p), nil
	})
}

type writerFunc func(p []byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// wsWriter frames bytes with a prefix and serialises writes to one WebSocket
// connection through a mutex shared by every writer on that connection.
type wsWriter struct {
	mu   *sync.Mutex
	c    *websocket.Conn
	ctx  context.Context
	kind byte
}

func (ww *wsWriter) Write(p []byte) (int, error) {
	ww.mu.Lock()
	defer ww.mu.Unlock()
	buf := make([]byte, 1+len(p))
	buf[0] = ww.kind
	copy(buf[1:], p)
	if err := ww.c.Write(ww.ctx, websocket.MessageBinary, buf); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (s *Server) execWS(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if id := r.URL.Query().Get("id"); id != "" {
		// Attaching to a session by id. The official CLI uses this for its
		// reconnectable mode (`sprite exec --no-stdin`). pooka keeps no attachable
		// sessions yet, so say so instead of a confusing "cmd is required".
		writeError(w, http.StatusNotFound, "session "+id+" not found: pooka does not support attaching to exec sessions yet; use `sprite exec --stdin` or `--http-post`")
		return
	}
	p, err := parseExecParams(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := s.Backend.Get(r.Context(), name); err != nil {
		writeBackendError(w, err)
		return
	}
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer c.CloseNow()
	c.SetReadLimit(16 << 20)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ss := s.addSession(name, p, cancel)
	defer s.endSession(ss.ID)

	wmu := &sync.Mutex{}
	stdout := &wsWriter{mu: wmu, c: c, ctx: ctx, kind: frameStdout}
	stderr := &wsWriter{mu: wmu, c: c, ctx: ctx, kind: frameStderr}
	writeText := func(v any) {
		b, _ := json.Marshal(v)
		wmu.Lock()
		_ = c.Write(ctx, websocket.MessageText, b)
		wmu.Unlock()
	}

	stdinR, stdinW := io.Pipe()
	resize := make(chan host.Winsize, 4)
	opts := backend.ExecOptions{Stdout: stdout, Stderr: stderr, Env: p.env, Dir: p.dir}
	if p.stdin || p.tty {
		opts.Stdin = stdinR
	}
	if p.tty {
		opts.PTY = &host.PTY{Size: p.size, Resize: resize}
	}
	writeText(map[string]any{"type": "session_info", "session_id": ss.ID, "command": ss.Command, "created": ss.Created.Unix(), "cols": p.size.Cols, "rows": p.size.Rows, "is_owner": true, "tty": p.tty})

	// Reader: client frames in, until the socket closes or the command ends.
	go func() {
		defer stdinW.Close()
		for {
			typ, data, err := c.Read(ctx)
			if err != nil {
				cancel()
				return
			}
			switch typ {
			case websocket.MessageBinary:
				if len(data) == 0 {
					continue
				}
				switch data[0] {
				case frameStdin:
					if _, err := stdinW.Write(data[1:]); err != nil {
						return
					}
				case frameStdinEOF:
					return
				}
			case websocket.MessageText:
				var msg struct {
					Type string `json:"type"`
					Cols uint16 `json:"cols"`
					Rows uint16 `json:"rows"`
				}
				if json.Unmarshal(data, &msg) == nil && msg.Type == "resize" {
					select {
					case resize <- host.Winsize{Rows: msg.Rows, Cols: msg.Cols}:
					default:
					}
				}
			}
		}
	}()

	code, err := s.Backend.Exec(ctx, name, p.cmd, opts)
	if err != nil {
		stderr.Write([]byte(err.Error() + "\n")) //nolint:errcheck
		code = 1
	}
	if code < 0 || code > 255 {
		code = 255
	}
	wmu.Lock()
	_ = c.Write(ctx, websocket.MessageBinary, []byte{frameExit, byte(code)})
	wmu.Unlock()
	writeText(map[string]any{"type": "exit", "exit_code": code})
	_ = c.Close(websocket.StatusNormalClosure, "")
}
