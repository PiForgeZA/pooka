package daemon

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/PiForgeZA/pooka/internal/backend"
	"github.com/PiForgeZA/pooka/internal/backend/backendtest"
	"github.com/PiForgeZA/pooka/internal/host"
	"github.com/coder/websocket"
)

func TestWebSocketResizeReachesBackend(t *testing.T) {
	got := make(chan host.Winsize, 1)
	b := execOverride{Backend: backendtest.New(), run: func(ctx context.Context, _ string, _ []string, o backend.ExecOptions) (int, error) {
		if o.PTY == nil {
			return 1, fmt.Errorf("missing PTY")
		}
		select {
		case size := <-o.PTY.Resize:
			got <- size
			return 0, nil
		case <-ctx.Done():
			return 1, ctx.Err()
		}
	}}
	ts := httptest.NewServer(New(b, "secret").Handler())
	defer ts.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, ts.URL+"/v1/sprites/dev1/exec?cmd=sh&tty=true", &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": []string{"Bearer secret"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	if err := c.Write(ctx, websocket.MessageText, []byte(`{"type":"resize","rows":41,"cols":123}`)); err != nil {
		t.Fatal(err)
	}
	select {
	case size := <-got:
		if size.Rows != 41 || size.Cols != 123 {
			t.Fatal(size)
		}
	case <-ctx.Done():
		t.Fatal("resize never reached backend")
	}
}

func TestPostExecWireChunks(t *testing.T) {
	b := execOverride{Backend: backendtest.New(), run: func(_ context.Context, _ string, _ []string, o backend.ExecOptions) (int, error) {
		io.WriteString(o.Stdout, "one")
		io.WriteString(o.Stderr, "two")
		return 7, nil
	}}
	ts := httptest.NewServer(New(b, "secret").Handler())
	defer ts.Close()
	c, err := net.DialTimeout("tcp", ts.Listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(2 * time.Second))
	fmt.Fprintf(c, "POST /v1/sprites/dev1/exec?cmd=true HTTP/1.1\r\nHost: local\r\nAuthorization: Bearer secret\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
	r := bufio.NewReader(c)
	line, err := r.ReadString('\n')
	if err != nil || !strings.Contains(line, "200 OK") {
		t.Fatalf("status=%q err=%v", line, err)
	}
	chunked := false
	for {
		line, err = r.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if line == "\r\n" {
			break
		}
		if strings.EqualFold(strings.TrimSpace(line), "Transfer-Encoding: chunked") {
			chunked = true
		}
	}
	if !chunked {
		t.Fatal("missing chunk framing")
	}
	for _, want := range []string{"\x01one", "\x02two", "\x03\x07"} {
		line, err = r.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		n, err := strconv.ParseInt(strings.TrimSpace(line), 16, 64)
		if err != nil || n > 1024 || n < 0 {
			t.Fatalf("chunk header=%q error=%v", line, err)
		}
		data := make([]byte, int(n)+2)
		if _, err := io.ReadFull(r, data); err != nil {
			t.Fatal(err)
		}
		if string(data) != want+"\r\n" {
			t.Fatalf("chunk=%q want=%q", data, want)
		}
	}
}
