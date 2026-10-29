package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/PiForgeZA/pooka/internal/backend"
	"github.com/coder/websocket"
)

func TestExecCloseBeforeExitRestoresTerminal(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		// A normal WebSocket close is still a failed exec without its exit frame.
		c.Close(websocket.StatusNormalClosure, "missing exit")
	}))
	defer ts.Close()
	c := New(ts.URL, "secret")
	ft := &fakeTerm{resize: make(chan struct{})}
	c.term = ft
	defer close(ft.resize)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	code, err := c.Exec(ctx, "dev1", []string{"true"}, backend.ExecOptions{TTY: true})
	if err == nil || code == 0 {
		t.Fatalf("close reported success: code=%d err=%v", code, err)
	}
	if !ft.raw || !ft.restore {
		t.Fatalf("terminal raw=%v restored=%v", ft.raw, ft.restore)
	}
}
