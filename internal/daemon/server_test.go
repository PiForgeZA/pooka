package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/PiForgeZA/pooka/internal/backend"
)

// fakeBackend is an in-memory Backend. Exec echoes stdin, prints args, honours env.
type fakeBackend struct {
	envs  map[string]backend.Env
	cps   map[string][]backend.Checkpoint
	calls []string
}

func newFakeBackend() *fakeBackend {
	return &fakeBackend{
		envs: map[string]backend.Env{
			"dev1": {Name: "dev1", VMID: 9100, Status: "running", IP: "10.0.0.5", Created: time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)},
			"dev2": {Name: "dev2", VMID: 9101, Status: "stopped"},
		},
		cps: map[string][]backend.Checkpoint{"dev1": {{ID: "cp-1", Time: time.Unix(1700000000, 0), Comment: "first"}}},
	}
}

func (f *fakeBackend) Create(_ context.Context, name string, _ backend.CreateOptions) (backend.Env, error) {
	if err := backend.ValidateName(name); err != nil {
		return backend.Env{}, err
	}
	if _, ok := f.envs[name]; ok {
		return backend.Env{}, backend.ErrExists
	}
	e := backend.Env{Name: name, VMID: 9102, Status: "running", IP: "10.0.0.7", Created: time.Now()}
	f.envs[name] = e
	return e, nil
}
func (f *fakeBackend) List(context.Context) ([]backend.Env, error) {
	out := []backend.Env{}
	for _, n := range []string{"dev1", "dev2", "dev3"} {
		if e, ok := f.envs[n]; ok {
			out = append(out, e)
		}
	}
	return out, nil
}
func (f *fakeBackend) Get(_ context.Context, name string) (backend.Env, error) {
	e, ok := f.envs[name]
	if !ok {
		return backend.Env{}, backend.ErrNotFound
	}
	return e, nil
}
func (f *fakeBackend) Start(context.Context, string) error { return nil }
func (f *fakeBackend) Stop(context.Context, string) error  { return nil }
func (f *fakeBackend) Destroy(_ context.Context, name string) error {
	if _, ok := f.envs[name]; !ok {
		return backend.ErrNotFound
	}
	delete(f.envs, name)
	return nil
}
func (f *fakeBackend) Exec(_ context.Context, name string, cmd []string, o backend.ExecOptions) (int, error) {
	if _, ok := f.envs[name]; !ok {
		return -1, backend.ErrNotFound
	}
	f.calls = append(f.calls, strings.Join(cmd, " "))
	if cmd[0] == "exit" {
		return 7, nil
	}
	if o.Stdin != nil {
		in, _ := io.ReadAll(o.Stdin)
		if len(in) > 0 {
			io.WriteString(o.Stdout, "stdin:"+string(in)+"\n")
		}
	}
	io.WriteString(o.Stdout, "cmd:"+strings.Join(cmd, " ")+"\n")
	if len(o.Env) > 0 {
		io.WriteString(o.Stdout, "env:"+strings.Join(o.Env, ",")+"\n")
	}
	if o.Stderr != nil {
		io.WriteString(o.Stderr, "err\n")
	}
	return 0, nil
}
func (f *fakeBackend) Push(context.Context, string, string, string) error { return nil }
func (f *fakeBackend) Pull(context.Context, string, string, string) error { return nil }
func (f *fakeBackend) Checkpoint(_ context.Context, name, comment string) (backend.Checkpoint, error) {
	cp := backend.Checkpoint{ID: "cp-2", Time: time.Now(), Comment: comment}
	f.cps[name] = append(f.cps[name], cp)
	return cp, nil
}
func (f *fakeBackend) ListCheckpoints(_ context.Context, name string) ([]backend.Checkpoint, error) {
	return f.cps[name], nil
}
func (f *fakeBackend) DeleteCheckpoint(context.Context, string, string) error { return nil }
func (f *fakeBackend) Restore(_ context.Context, name, id string) error {
	f.calls = append(f.calls, "restore "+name+" "+id)
	return nil
}

func newTestServer(t *testing.T) (*httptest.Server, *fakeBackend) {
	t.Helper()
	fb := newFakeBackend()
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
	if _, ok := fb.envs["dev3"]; ok {
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
	if fb.cps["dev1"][1].Comment != "hello" {
		t.Errorf("comment not passed: %+v", fb.cps["dev1"])
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
	if fb.calls[len(fb.calls)-1] != "restore dev1 cp-1" {
		t.Errorf("calls: %v", fb.calls)
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
	if fb.calls[0] != "bash -l" {
		t.Errorf("console must map to a login shell, got %v", fb.calls)
	}
}

func TestFsEndpoints(t *testing.T) {
	ts, fb := newTestServer(t)
	resp, b := do(t, ts, "GET", "/v1/sprites/dev1/fs/read?path=notes.txt&workingDir=/home/pooka", nil, "secret")
	if resp.StatusCode != 200 || !strings.Contains(string(b), "cmd:cat -- /home/pooka/notes.txt") {
		t.Errorf("read: %d %s", resp.StatusCode, b)
	}
	resp, b = do(t, ts, "PUT", "/v1/sprites/dev1/fs/write?path=/usr/share/terminfo/x/xterm&mkdirParents=true&mode=0644&workingDir=/", strings.NewReader("data"), "secret")
	if resp.StatusCode != 200 {
		t.Errorf("write: %d %s", resp.StatusCode, b)
	}
	last := fb.calls[len(fb.calls)-1]
	if !strings.Contains(last, "mkdir -p") || !strings.Contains(last, "/usr/share/terminfo/x/xterm 0644") {
		t.Errorf("write cmd: %s", last)
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
