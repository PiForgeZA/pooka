package daemon

import (
	"context"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/PiForgeZA/pooka/internal/backend"
	"github.com/PiForgeZA/pooka/internal/backend/backendtest"
)

func TestWriteBodyLimit(t *testing.T) {
	for _, tc := range []struct {
		name    string
		size    int
		unknown bool
		status  int
	}{{"boundary", maxWriteBody, false, 200}, {"over", maxWriteBody + 1, false, 413}, {"chunked", maxWriteBody + 1, true, 413}} {
		t.Run(tc.name, func(t *testing.T) {
			fb := backendtest.New()
			s := New(fb, "secret")
			req := httptest.NewRequest("PUT", "/v1/sprites/dev1/fs/write?path=/tmp/test", strings.NewReader(strings.Repeat("x", tc.size)))
			if tc.unknown {
				req.ContentLength = -1
			}
			req.Header.Set("Authorization", "Bearer secret")
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, req)
			if w.Code != tc.status {
				t.Fatalf("status=%d body=%s", w.Code, w.Body)
			}
			if tc.status == 413 && len(fb.Calls) != 0 {
				t.Fatalf("oversized write reached backend: %v", fb.Calls)
			}
			if tc.status == 200 && len(fb.Pushed["/tmp/test"]) != tc.size {
				t.Fatal("accepted write did not preserve the complete body")
			}
		})
	}
}

func TestExecBodyLimit(t *testing.T) {
	for _, size := range []int{maxPostBody, maxPostBody + 1} {
		fb := backendtest.New()
		read := 0
		var readErr error
		b := execOverride{Backend: fb, run: func(_ context.Context, _ string, _ []string, o backend.ExecOptions) (int, error) {
			data, err := io.ReadAll(o.Stdin)
			read = len(data)
			readErr = err
			return 0, err
		}}
		s := New(b, "secret")
		req := httptest.NewRequest("POST", "/v1/sprites/dev1/exec?cmd=cat&stdin=true", strings.NewReader(strings.Repeat("x", size)))
		req.Header.Set("Authorization", "Bearer secret")
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		if read != maxPostBody || (readErr != nil) != (size > maxPostBody) {
			t.Fatalf("size=%d read=%d error=%v", size, read, readErr)
		}
		data := w.Body.Bytes()
		want := byte(0)
		if size > maxPostBody {
			want = 1
		}
		if len(data) < 2 || data[len(data)-2] != 3 || data[len(data)-1] != want {
			t.Fatalf("missing exit status %d", want)
		}
	}
}

func TestProtectedRoutesRejectBeforeBackend(t *testing.T) {
	routes := [][2]string{{"GET", "/v1/sprites"}, {"POST", "/v1/sprites"}, {"DELETE", "/v1/sprites/dev1"}, {"POST", "/v1/sprites/dev1/checkpoint"}, {"POST", "/v1/sprites/dev1/checkpoints/cp-1/restore"}, {"PUT", "/v1/sprites/dev1/fs/write?path=/tmp/x"}, {"GET", "/v1/sprites/dev1/fs/read?path=/tmp/x"}, {"POST", "/v1/sprites/dev1/exec?cmd=true"}, {"GET", "/v1/sprites/dev1/exec?cmd=true"}, {"POST", "/pooka/v1/sprites/dev1/start"}, {"PUT", "/pooka/v1/sprites/dev1/settings"}}
	for _, r := range routes {
		for _, token := range []string{"", "wrong"} {
			t.Run(r[0]+r[1]+token, func(t *testing.T) {
				fb := backendtest.New()
				s := New(fb, "secret")
				req := httptest.NewRequest(r[0], r[1], nil)
				if token != "" {
					req.Header.Set("Authorization", "Bearer "+token)
				}
				if r[0] == "GET" && strings.Contains(r[1], "exec") {
					req.Header.Set("Upgrade", "websocket")
					req.Header.Set("Connection", "Upgrade")
				}
				w := httptest.NewRecorder()
				s.Handler().ServeHTTP(w, req)
				if w.Code != 401 || len(fb.Calls) != 0 {
					t.Fatalf("status=%d calls=%v", w.Code, fb.Calls)
				}
			})
		}
	}
}

func TestKillSessionIsScopedToEnvironment(t *testing.T) {
	s := New(backendtest.New(), "secret")
	a, b := 0, 0
	ss := s.addSession("dev1", execParams{}, func() { a++ })
	s.addSession("dev2", execParams{}, func() { b++ })
	for _, tc := range []struct {
		name          string
		status, calls int
	}{{"dev2", 404, 0}, {"dev1", 200, 1}} {
		req := httptest.NewRequest("POST", "/v1/sprites/"+tc.name+"/exec/"+ss.ID+"/kill", nil)
		req.Header.Set("Authorization", "Bearer secret")
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		if w.Code != tc.status || a != tc.calls || b != 0 {
			t.Fatalf("status=%d cancels=%d/%d", w.Code, a, b)
		}
	}
}
