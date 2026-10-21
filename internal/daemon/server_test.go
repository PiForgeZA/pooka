package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/PiForgeZA/pooka/internal/backend"
	"github.com/PiForgeZA/pooka/internal/backend/backendtest"
)

func newTestServer(t *testing.T) (*httptest.Server, *backendtest.Fake) {
	t.Helper()
	fb := backendtest.New()
	s := New(fb, "secret")
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts, fb
}

func do(t *testing.T, ts *httptest.Server, method, path string, body io.Reader, token string) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest(method, ts.URL+path, body)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, b
}

func TestAuthRequired(t *testing.T) {
	ts, _ := newTestServer(t)
	if resp, _ := do(t, ts, "GET", "/v1/sprites", nil, ""); resp.StatusCode != 401 {
		t.Errorf("no token: %d", resp.StatusCode)
	}
	if resp, _ := do(t, ts, "GET", "/v1/sprites", nil, "wrong"); resp.StatusCode != 401 {
		t.Errorf("wrong token: %d", resp.StatusCode)
	}
	if resp, _ := do(t, ts, "GET", "/healthz", nil, ""); resp.StatusCode != 200 {
		t.Errorf("healthz must be open: %d", resp.StatusCode)
	}
}

func TestListAndGetSprites(t *testing.T) {
	ts, _ := newTestServer(t)
	resp, b := do(t, ts, "GET", "/v1/sprites?max_results=100", nil, "secret")
	if resp.StatusCode != 200 {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
	var list spriteListResponse
	json.Unmarshal(b, &list)
	if len(list.Sprites) != 2 || list.Running != 1 || list.Cold != 1 || list.Name != "local" {
		t.Errorf("list: %+v", list)
	}
	if list.Sprites[0].Status != "running" || list.Sprites[0].URL != "http://10.0.0.5:8080" || list.Sprites[0].ID != "sprite-9100" {
		t.Errorf("dev1: %+v", list.Sprites[0])
	}
	if list.Sprites[1].Status != "cold" || list.Sprites[1].URL != "" {
		t.Errorf("dev2: %+v", list.Sprites[1])
	}
	resp, b = do(t, ts, "GET", "/v1/sprites/nope", nil, "secret")
	if resp.StatusCode != 404 {
		t.Errorf("missing sprite: %d %s", resp.StatusCode, b)
	}
}

func TestCreateAndDelete(t *testing.T) {
	ts, fb := newTestServer(t)
	resp, b := do(t, ts, "POST", "/v1/sprites", strings.NewReader(`{"name":"dev3"}`), "secret")
	if resp.StatusCode != 201 {
		t.Fatalf("create: %d %s", resp.StatusCode, b)
	}
	var sp spriteResponse
	json.Unmarshal(b, &sp)
	if sp.Name != "dev3" || sp.Status != "running" || sp.Organization != "local" || sp.CreatedAt == "" {
		t.Errorf("created: %+v", sp)
	}
	if resp, _ := do(t, ts, "POST", "/v1/sprites", strings.NewReader(`{"name":"dev3"}`), "secret"); resp.StatusCode != 409 {
		t.Errorf("duplicate: %d", resp.StatusCode)
	}
	if resp, _ := do(t, ts, "POST", "/v1/sprites", strings.NewReader(`{"name":"Bad Name"}`), "secret"); resp.StatusCode != 400 {
		t.Errorf("bad name: %d", resp.StatusCode)
	}
	if resp, _ := do(t, ts, "DELETE", "/v1/sprites/dev3", nil, "secret"); resp.StatusCode != 204 {
		t.Errorf("delete: %d", resp.StatusCode)
	}
	if _, ok := fb.Envs["dev3"]; ok {
		t.Error("dev3 still exists")
	}
}

func TestCheckpointStreamAndList(t *testing.T) {
	ts, fb := newTestServer(t)
	resp, b := do(t, ts, "POST", "/v1/sprites/dev1/checkpoint", strings.NewReader(`{"comment":"hello"}`), "secret")
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "application/x-ndjson" {
		t.Fatalf("checkpoint: %d %s %s", resp.StatusCode, resp.Header.Get("Content-Type"), b)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	var last streamEvent
	json.Unmarshal([]byte(lines[len(lines)-1]), &last)
	if last.Type != "complete" || !strings.Contains(last.Data, "cp-2") {
		t.Errorf("stream: %s", b)
	}
	if fb.Cps["dev1"][1].Comment != "hello" {
		t.Errorf("comment not passed: %+v", fb.Cps["dev1"])
	}

	resp, b = do(t, ts, "GET", "/v1/sprites/dev1/checkpoints", nil, "secret")
	var cps []checkpointInfo
	json.Unmarshal(b, &cps)
	if resp.StatusCode != 200 || len(cps) != 2 || cps[0].ID != "cp-2" || cps[1].Comment != "first" {
		t.Errorf("list: %d %+v", resp.StatusCode, cps)
	}

	resp, b = do(t, ts, "POST", "/v1/sprites/dev1/checkpoints/cp-1/restore", nil, "secret")
	if resp.StatusCode != 200 || !strings.Contains(string(b), `"complete"`) {
		t.Errorf("restore: %d %s", resp.StatusCode, b)
	}
	if fb.Calls[len(fb.Calls)-1] != "restore dev1 cp-1" {
		t.Errorf("calls: %v", fb.Calls)
	}
}

// parseFrames splits a framed exec body. Frames are only separable by their HTTP
// chunks on the wire, so the test relies on the fake backend writing stdout once
// and the exit frame being the final two bytes.
func TestExecPost(t *testing.T) {
	ts, _ := newTestServer(t)
	resp, b := do(t, ts, "POST", "/v1/sprites/dev1/exec?cmd=echo&cmd=hi&path=echo&stdin=true", strings.NewReader("input"), "secret")
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "application/octet-stream" {
		t.Fatalf("%d %s %q", resp.StatusCode, resp.Header.Get("Content-Type"), b)
	}
	if b[0] != frameStdout || !strings.Contains(string(b), "stdin:input") || !strings.Contains(string(b), "cmd:echo hi") {
		t.Errorf("body: %q", b)
	}
	if n := len(b); b[n-2] != frameExit || b[n-1] != 0 {
		t.Errorf("exit frame: % x", b[len(b)-2:])
	}
	_, b = do(t, ts, "POST", "/v1/sprites/dev1/exec?cmd=exit", nil, "secret")
	if n := len(b); b[n-2] != frameExit || b[n-1] != 7 {
		t.Errorf("exit code frame: % x", b[len(b)-2:])
	}
	if resp, _ := do(t, ts, "POST", "/v1/sprites/nope/exec?cmd=true", nil, "secret"); resp.StatusCode != 404 {
		t.Errorf("missing env: %d", resp.StatusCode)
	}
}

func TestExecAttachNotSupported(t *testing.T) {
	ts, _ := newTestServer(t)
	req, _ := http.NewRequest("GET", ts.URL+"/v1/sprites/dev1/exec?id=3&output_offset=0&stdin=false", nil)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "Upgrade")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 404 || !strings.Contains(string(b), "--stdin") {
		t.Errorf("%d %s", resp.StatusCode, b)
	}
}

func TestExecParamsSize(t *testing.T) {
	r := httptest.NewRequest("GET", "/v1/sprites/x/exec?cmd=bash&tty=true&rows=50&cols=200", nil)
	p, err := parseExecParams(r)
	if err != nil || p.size.Rows != 50 || p.size.Cols != 200 || !p.tty {
		t.Errorf("%+v %v", p, err)
	}
	r = httptest.NewRequest("GET", "/v1/sprites/x/exec?cmd=bash", nil)
	p, _ = parseExecParams(r)
	if p.size.Rows != 24 || p.size.Cols != 80 {
		t.Errorf("defaults: %+v", p.size)
	}
}

func TestExecWebSocket(t *testing.T) {
	ts, fb := newTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(ts.URL, "http") + "/v1/sprites/dev1/exec?cmd=" + consoleCmd + "&stdin=true&tty=true&env=TERM%3Dxterm"
	c, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer secret"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()

	// First message: session_info as text.
	typ, data, err := c.Read(ctx)
	if err != nil || typ != websocket.MessageText || !strings.Contains(string(data), `"session_info"`) {
		t.Fatalf("session_info: %v %s", err, data)
	}
	// Send a resize, some stdin, then EOF.
	c.Write(ctx, websocket.MessageText, []byte(`{"type":"resize","cols":100,"rows":30}`))
	c.Write(ctx, websocket.MessageBinary, append([]byte{frameStdin}, []byte("hello")...))
	c.Write(ctx, websocket.MessageBinary, []byte{frameStdinEOF})

	var stdout bytes.Buffer
	var exitCode = -1
	var exitMsg bool
	for exitCode < 0 || !exitMsg {
		typ, data, err := c.Read(ctx)
		if err != nil {
			break
		}
		switch typ {
		case websocket.MessageBinary:
			switch data[0] {
			case frameStdout:
				stdout.Write(data[1:])
			case frameExit:
				exitCode = int(data[1])
			}
		case websocket.MessageText:
			if strings.Contains(string(data), `"exit"`) {
				exitMsg = true
			}
		}
	}
	if exitCode != 0 || !exitMsg {
		t.Errorf("exit: code=%d msg=%v", exitCode, exitMsg)
	}
	out := stdout.String()
	if !strings.Contains(out, "stdin:hello") || !strings.Contains(out, "cmd:bash -l") || !strings.Contains(out, "env:TERM=xterm") {
		t.Errorf("stdout: %q", out)
	}
	if fb.Calls[0] != "bash -l" {
		t.Errorf("console must map to a login shell, got %v", fb.Calls)
	}
}

func TestFsEndpoints(t *testing.T) {
	ts, fb := newTestServer(t)
	resp, b := do(t, ts, "PUT", "/v1/sprites/dev1/fs/write?path=/usr/share/terminfo/x/xterm&mkdirParents=true&mode=0644&workingDir=/", strings.NewReader("data"), "secret")
	if resp.StatusCode != 200 || !strings.Contains(string(b), `"size":4`) {
		t.Errorf("write: %d %s", resp.StatusCode, b)
	}
	last := fb.Calls[len(fb.Calls)-1]
	if !strings.Contains(last, "mkdir -p") || !strings.Contains(last, "/usr/share/terminfo/x/xterm 0644") {
		t.Errorf("write cmd: %s", last)
	}
	resp, b = do(t, ts, "GET", "/v1/sprites/dev1/fs/read?path=xterm&workingDir=/usr/share/terminfo/x", nil, "secret")
	if resp.StatusCode != 200 || string(b) != "data" {
		t.Errorf("read: %d %q", resp.StatusCode, b)
	}
	if resp, _ := do(t, ts, "GET", "/v1/sprites/dev1/fs/read?path=/nope", nil, "secret"); resp.StatusCode != 404 {
		t.Errorf("read missing: %d", resp.StatusCode)
	}
	if resp, _ := do(t, ts, "PUT", "/v1/sprites/dev1/fs/write?path=/x&mode=zz", nil, "secret"); resp.StatusCode != 400 {
		t.Errorf("bad mode: %d", resp.StatusCode)
	}
	if resp, _ := do(t, ts, "GET", "/v1/sprites/dev1/fs/list?workingDir=/", nil, "secret"); resp.StatusCode != 400 {
		t.Errorf("list without path: %d", resp.StatusCode)
	}
}

func TestParseFind(t *testing.T) {
	out := "a.txt\tf\t12\t0644\t1700000000.5\nsub\td\t4096\t0755\t1700000001\nlink\tl\t3\t0777\t1700000002\n"
	es := parseFind("/home/pooka", out)
	if len(es) != 3 || es[0].Path != "/home/pooka/a.txt" || es[0].Type != "file" || es[0].Size != 12 || es[1].IsDir != true || es[2].Type != "symlink" {
		t.Errorf("%+v", es)
	}
	if es[0].ModTime != "2023-11-14T22:13:20Z" {
		t.Errorf("modTime %s", es[0].ModTime)
	}
}

func TestNotImplemented(t *testing.T) {
	ts, _ := newTestServer(t)
	resp, b := do(t, ts, "GET", "/v1/sprites/dev1/services", nil, "secret")
	if resp.StatusCode != 501 || !strings.Contains(string(b), "not implemented") {
		t.Errorf("%d %s", resp.StatusCode, b)
	}
}

func TestPookaExtensions(t *testing.T) {
	ts, fb := newTestServer(t)
	s := New(fb, "secret")
	s.Create = backend.CreateOptions{Cores: 4, MemoryMB: 8192, DiskGB: 32, SSHPubKey: "daemon-key"}
	ts.Config.Handler = s.Handler()

	// Create with options: zero values keep the daemon defaults, a client key wins.
	resp, b := do(t, ts, "POST", "/pooka/v1/sprites", strings.NewReader(`{"name":"dev3","cores":2,"ssh_pubkey":"client-key"}`), "secret")
	if resp.StatusCode != 201 {
		t.Fatalf("create: %d %s", resp.StatusCode, b)
	}
	if fb.Created.Cores != 2 || fb.Created.MemoryMB != 8192 || fb.Created.DiskGB != 32 || fb.Created.SSHPubKey != "client-key" {
		t.Errorf("create options: %+v", fb.Created)
	}
	var sp spriteResponse
	json.Unmarshal(b, &sp)
	if sp.Name != "dev3" || sp.ID != "sprite-9102" {
		t.Errorf("created: %+v", sp)
	}
	if resp, _ := do(t, ts, "POST", "/pooka/v1/sprites", strings.NewReader(`{"name":"dev3"}`), "secret"); resp.StatusCode != 409 {
		t.Errorf("duplicate: %d", resp.StatusCode)
	}
	if resp, _ := do(t, ts, "POST", "/pooka/v1/sprites", strings.NewReader(`{}`), "secret"); resp.StatusCode != 400 {
		t.Errorf("no name: %d", resp.StatusCode)
	}

	if resp, b := do(t, ts, "POST", "/pooka/v1/sprites/dev2/start", nil, "secret"); resp.StatusCode != 204 {
		t.Errorf("start: %d %s", resp.StatusCode, b)
	}
	if resp, b := do(t, ts, "POST", "/pooka/v1/sprites/dev1/stop", nil, "secret"); resp.StatusCode != 204 {
		t.Errorf("stop: %d %s", resp.StatusCode, b)
	}
	if resp, _ := do(t, ts, "POST", "/pooka/v1/sprites/nope/start", nil, "secret"); resp.StatusCode != 404 {
		t.Errorf("start missing: %d", resp.StatusCode)
	}
	if resp, b := do(t, ts, "DELETE", "/pooka/v1/sprites/dev1/checkpoints/cp-1", nil, "secret"); resp.StatusCode != 204 {
		t.Errorf("delete checkpoint: %d %s", resp.StatusCode, b)
	}
	if resp, _ := do(t, ts, "DELETE", "/pooka/v1/sprites/dev1/checkpoints/cp-1", nil, "secret"); resp.StatusCode != 404 {
		t.Errorf("delete missing checkpoint: %d", resp.StatusCode)
	}
	want := []string{"create dev3", "start dev2", "stop dev1", "delcp dev1 cp-1"}
	if strings.Join(fb.Calls, ",") != strings.Join(want, ",") {
		t.Errorf("calls %v, want %v", fb.Calls, want)
	}
	if resp, _ := do(t, ts, "GET", "/pooka/v1/sprites/dev1/start", nil, ""); resp.StatusCode != 401 {
		t.Errorf("extensions must need auth: %d", resp.StatusCode)
	}
	if resp, b := do(t, ts, "GET", "/pooka/v1/nothing", nil, "secret"); resp.StatusCode != 404 || !strings.Contains(string(b), "error") {
		t.Errorf("unknown extension: %d %s", resp.StatusCode, b)
	}
}

func TestFsChown(t *testing.T) {
	ts, fb := newTestServer(t)
	body := `{"path":"notes.txt","workingDir":"/home/pooka","uid":"pooka","gid":1000,"recursive":true,"asRoot":true}`
	resp, b := do(t, ts, "POST", "/v1/sprites/dev1/fs/chown", strings.NewReader(body), "secret")
	if resp.StatusCode != 200 || !strings.Contains(string(b), `"count":1`) {
		t.Errorf("chown: %d %s", resp.StatusCode, b)
	}
	last := fb.Calls[len(fb.Calls)-1]
	if !strings.Contains(last, "chown -R") || !strings.HasSuffix(last, "pooka:1000 /home/pooka/notes.txt") {
		t.Errorf("chown cmd: %s", last)
	}
	if resp, _ := do(t, ts, "POST", "/v1/sprites/dev1/fs/chown", strings.NewReader(`{"path":"/x"}`), "secret"); resp.StatusCode != 400 {
		t.Errorf("chown without owner: %d", resp.StatusCode)
	}
	if resp, _ := do(t, ts, "POST", "/v1/sprites/dev1/fs/chown", strings.NewReader(`{"path":"/x","uid":"a b"}`), "secret"); resp.StatusCode != 400 {
		t.Errorf("chown bad owner: %d", resp.StatusCode)
	}
}

func TestExecRootOnlyViaExtension(t *testing.T) {
	ts, fb := newTestServer(t)
	if resp, _ := do(t, ts, "POST", "/pooka/v1/sprites/dev1/exec?cmd=id&root=true", nil, "secret"); resp.StatusCode != 200 {
		t.Fatalf("extension exec: %d", resp.StatusCode)
	}
	if resp, _ := do(t, ts, "POST", "/v1/sprites/dev1/exec?cmd=id&root=true", nil, "secret"); resp.StatusCode != 200 {
		t.Fatalf("spec exec: %d", resp.StatusCode)
	}
	if len(fb.Root) != 2 || !fb.Root[0] || fb.Root[1] {
		t.Errorf("root must apply on /pooka/v1 only: %v", fb.Root)
	}
}

// After stdin EOF the daemon must still notice the client going away and stop the
// command. Otherwise a silent long-running command outlives its session.
func TestExecWebSocketDisconnectAfterStdinEOF(t *testing.T) {
	fb := backendtest.New()
	s := New(fb, "secret")
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(ts.URL, "http") + "/v1/sprites/dev1/exec?cmd=sleep&stdin=true"
	c, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer secret"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Read(ctx); err != nil { // session_info
		t.Fatal(err)
	}
	c.Write(ctx, websocket.MessageBinary, []byte{frameStdinEOF})
	time.Sleep(100 * time.Millisecond) // let the server process the EOF first
	c.CloseNow()
	deadline := time.Now().Add(3 * time.Second)
	for {
		s.mu.Lock()
		n := len(s.sessions)
		s.mu.Unlock()
		if n == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("session still alive after the client disconnected")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestLogRequestsOmitsQuery(t *testing.T) {
	var buf bytes.Buffer
	h := LogRequests(log.New(&buf, "", 0), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
	req := httptest.NewRequest("GET", "/v1/sprites/dev1/exec?cmd=env&env=SECRET%3Dhunter2&token=tok123", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)
	if got := buf.String(); !strings.Contains(got, "GET /v1/sprites/dev1/exec") || strings.Contains(got, "hunter2") || strings.Contains(got, "tok123") {
		t.Errorf("log line: %q", got)
	}
	buf.Reset()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/healthz", nil))
	if buf.Len() != 0 {
		t.Errorf("healthz must not be logged: %q", buf.String())
	}
}
