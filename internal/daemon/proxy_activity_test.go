package daemon

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/PiForgeZA/pooka/internal/proxy"
)

func TestProxyRequestDelaysIdleStop(t *testing.T) {
	s, fb, now := newIdleServer(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("hello")) }))
	defer upstream.Close()
	host, port, err := net.SplitHostPort(upstream.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.Atoi(port)
	if err != nil {
		t.Fatal(err)
	}
	e := fb.Envs["dev1"]
	e.IP = host
	fb.Envs["dev1"] = e
	p := &proxy.Proxy{Backend: fb, Org: "local", Domain: "test.local", Token: "secret", Port: n, Touch: s.Touch}
	*now = now.Add(25 * time.Minute)
	req := httptest.NewRequest("GET", "http://dev1-local.test.local/", nil)
	req.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	p.ServeHTTP(w, req)
	if w.Code != 200 || w.Body.String() != "hello" {
		t.Fatalf("status=%d body=%s", w.Code, w.Body)
	}
	*now = now.Add(10 * time.Minute)
	if got := s.idleTick(context.Background()); len(got) != 0 {
		t.Fatalf("stopped after proxy activity: %v", got)
	}
	*now = now.Add(21 * time.Minute)
	if got := s.idleTick(context.Background()); len(got) != 1 || got[0] != "dev1" {
		t.Fatalf("did not expire after activity: %v", got)
	}
}
