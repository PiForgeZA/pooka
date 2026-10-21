package daemon

import (
	"log"
	"net/http"
	"time"
)

// LogRequests logs one line per request: remote address, method, path and
// duration. The query string is left out on purpose: it can carry the bearer
// token (`token=`) and exec environment values (`env=`). /healthz is not logged.
func LogRequests(l *log.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		if r.URL.Path != "/healthz" {
			l.Printf("%s %s %s %s", r.RemoteAddr, r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
		}
	})
}
