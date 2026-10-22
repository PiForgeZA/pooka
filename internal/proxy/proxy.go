// Package proxy serves environment URLs (ADR 0008, SPEC section 18). It routes
// https://<name>-<org>.<domain> to port 8080 inside the environment, wakes a
// stopped environment on request, checks the URL auth setting, and serves the
// Sprites API for the host name api.<domain>.
package proxy

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/PiForgeZA/pooka/internal/backend"
)

// CookieName carries the per-host auth cookie.
const CookieName = "pooka_auth"

// TokenParam is the query parameter that sets the cookie.
const TokenParam = "token"

// Proxy is an http.Handler for the proxy listener.
type Proxy struct {
	Backend backend.Backend
	Org     string // "local"
	Domain  string // DNS suffix; only used for the API host name
	Token   string // the daemon bearer token; auth "sprite" requires it
	// API, when set, serves requests for api.<domain>.
	API http.Handler
	// Port is the port inside the environment. Default 8080.
	Port int
	// StartWait bounds a wake: start plus waiting for the port. Default 30 s.
	StartWait time.Duration
	// Poll is the interval between connection attempts while waking. Default 250 ms.
	Poll time.Duration
	// CacheTTL bounds how long a lookup is reused. Default 2 s.
	CacheTTL time.Duration
	// Touch records activity for the idle loop. nil ignores it.
	Touch func(name string)
	Log   *log.Logger
	// Dial opens connections to environments. nil means net.Dialer.
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
	// Now returns the current time; tests set it.
	Now func() time.Time

	mu        sync.Mutex
	cache     map[string]cached
	waking    map[string]*wake
	initRun   sync.Once
	transport *http.Transport
}

// cached is one lookup result. err holds ErrNotFound so unknown names do not
// cost a host call per request.
type cached struct {
	env backend.Env
	err error
	at  time.Time
}

type wake struct {
	done chan struct{}
	err  error
}

func (p *Proxy) init() {
	p.initRun.Do(func() {
		if p.Port == 0 {
			p.Port = 8080
		}
		if p.StartWait == 0 {
			p.StartWait = 30 * time.Second
		}
		if p.Poll == 0 {
			p.Poll = 250 * time.Millisecond
		}
		if p.CacheTTL == 0 {
			p.CacheTTL = 2 * time.Second
		}
		if p.Log == nil {
			p.Log = log.Default()
		}
		if p.Dial == nil {
			p.Dial = (&net.Dialer{Timeout: 2 * time.Second}).DialContext
		}
		if p.Now == nil {
			p.Now = time.Now
		}
		p.cache = map[string]cached{}
		p.waking = map[string]*wake{}
		// One transport for every request. Idle keep-alive connections into an
		// environment close after a few seconds, so the idle loop's check for
		// established connections (ADR 0009) does not see the proxy's own.
		p.transport = &http.Transport{DialContext: p.Dial, IdleConnTimeout: 5 * time.Second, MaxIdleConnsPerHost: 4}
	})
}

// SplitHost returns the environment name and org from a URL host such as
// "web-local.192-168-68-200.sslip.io". ok is false when the first label has no
// hyphen. The suffix is ignored: any name that reaches the host works.
func SplitHost(host string) (name, org string, ok bool) {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	label, _, _ := strings.Cut(host, ".")
	i := strings.LastIndex(label, "-")
	if i <= 0 || i == len(label)-1 {
		return "", "", false
	}
	return label[:i], label[i+1:], true
}

func hostname(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return host
}

// CookieValue is the per-host auth cookie: the hex HMAC-SHA256 of the host name
// with the token as key. It never contains the token and is valid for one host.
func CookieValue(token, host string) string {
	m := hmac.New(sha256.New, []byte(token))
	m.Write([]byte(strings.ToLower(hostname(host))))
	return hex.EncodeToString(m.Sum(nil))
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.init()
	host := strings.ToLower(hostname(r.Host))
	if p.API != nil && p.Domain != "" && host == "api."+strings.ToLower(p.Domain) {
		p.API.ServeHTTP(w, r)
		return
	}
	name, org, ok := SplitHost(host)
	if !ok || org != p.Org {
		http.Error(w, fmt.Sprintf("pooka: no environment for host %q: use <name>-%s.<domain>", host, p.Org), http.StatusNotFound)
		return
	}
	env, err := p.lookup(r.Context(), name, false)
	if err != nil {
		p.fail(w, name, err)
		return
	}
	if env.URLAuth != "public" && !p.authorized(w, r, host) {
		return
	}
	if p.Touch != nil {
		p.Touch(name)
	}
	target, err := p.ready(r.Context(), env)
	if err != nil {
		p.fail(w, name, err)
		return
	}
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.Host = pr.In.Host // the app sees its public name
			pr.SetXForwarded()
		},
		Transport: p.transport,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			p.fail(w, name, fmt.Errorf("port %d: %w", p.Port, err))
		},
		ErrorLog: p.Log,
	}
	rp.ServeHTTP(w, r)
}

// authorized checks the bearer header, the cookie, or ?token=. It writes the
// response and returns false when the request may not continue.
func (p *Proxy) authorized(w http.ResponseWriter, r *http.Request, host string) bool {
	if p.Token == "" {
		return true
	}
	if t := r.URL.Query().Get(TokenParam); t != "" {
		if subtle.ConstantTimeCompare([]byte(t), []byte(p.Token)) != 1 {
			http.Error(w, "pooka: wrong token", http.StatusUnauthorized)
			return false
		}
		http.SetCookie(w, &http.Cookie{
			Name: CookieName, Value: CookieValue(p.Token, host), Path: "/",
			MaxAge: int((30 * 24 * time.Hour).Seconds()), Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode,
		})
		clean := *r.URL
		q := clean.Query()
		q.Del(TokenParam)
		clean.RawQuery = q.Encode()
		clean.Scheme, clean.Host = "", ""
		http.Redirect(w, r, clean.RequestURI(), http.StatusSeeOther)
		return false
	}
	if b, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok && subtle.ConstantTimeCompare([]byte(strings.TrimSpace(b)), []byte(p.Token)) == 1 {
		return true
	}
	if c, err := r.Cookie(CookieName); err == nil && subtle.ConstantTimeCompare([]byte(c.Value), []byte(CookieValue(p.Token, host))) == 1 {
		return true
	}
	http.Error(w, "pooka: this environment accepts token holders only. Open the URL that `pooka url <name>` prints, or send Authorization: Bearer <token>.", http.StatusUnauthorized)
	return false
}

// lookup returns the environment, from the cache unless fresh is set.
func (p *Proxy) lookup(ctx context.Context, name string, fresh bool) (backend.Env, error) {
	now := p.Now()
	if !fresh {
		p.mu.Lock()
		c, ok := p.cache[name]
		p.mu.Unlock()
		if ok && now.Sub(c.at) < p.CacheTTL {
			return c.env, c.err
		}
	}
	env, err := p.Backend.Get(ctx, name)
	if err != nil && !errors.Is(err, backend.ErrNotFound) {
		return backend.Env{}, err
	}
	p.mu.Lock()
	p.cache[name] = cached{env: env, err: err, at: now}
	p.mu.Unlock()
	return env, err
}

// ready returns the target URL once port Port answers. A stopped environment is
// started first; one start runs per name and other requests wait for it. A
// running environment that does not answer fails at once.
func (p *Proxy) ready(ctx context.Context, env backend.Env) (*url.URL, error) {
	woke := false
	if env.Status != "running" {
		if err := p.start(ctx, env.Name); err != nil {
			// Someone else may have started it between the lookup and now.
			if fresh, lerr := p.lookup(ctx, env.Name, true); lerr != nil || fresh.Status != "running" {
				return nil, fmt.Errorf("start: %w", err)
			}
		}
		woke = true
	}
	deadline := p.Now().Add(p.StartWait)
	for {
		if env.IP == "" || env.Status != "running" {
			var err error
			if env, err = p.lookup(ctx, env.Name, true); err != nil {
				return nil, err
			}
			if env.IP == "" && !woke {
				return nil, fmt.Errorf("%s is running but has no IP address yet", env.Name)
			}
		}
		if env.IP != "" {
			addr := net.JoinHostPort(env.IP, fmt.Sprint(p.Port))
			if conn, err := p.Dial(ctx, "tcp", addr); err == nil {
				conn.Close()
				return &url.URL{Scheme: "http", Host: addr}, nil
			} else if !woke {
				return nil, fmt.Errorf("nothing listens on port %d in %s: %w", p.Port, env.Name, err)
			}
		}
		if p.Now().After(deadline) {
			return nil, fmt.Errorf("%s started but port %d did not answer within %s", env.Name, p.Port, p.StartWait)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(p.Poll):
		}
	}
}

// start runs Backend.Start once per name; concurrent callers share the result.
func (p *Proxy) start(ctx context.Context, name string) error {
	p.mu.Lock()
	wk, running := p.waking[name]
	if !running {
		wk = &wake{done: make(chan struct{})}
		p.waking[name] = wk
	}
	p.mu.Unlock()
	if running {
		select {
		case <-wk.done:
			return wk.err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	p.Log.Printf("proxy: waking %s", name)
	// The start outlives the request that triggered it: a client that gives up
	// must not leave a half-started environment.
	wk.err = p.Backend.Start(context.WithoutCancel(ctx), name)
	p.mu.Lock()
	delete(p.waking, name)
	delete(p.cache, name)
	p.mu.Unlock()
	close(wk.done)
	return wk.err
}

func (p *Proxy) fail(w http.ResponseWriter, name string, err error) {
	if errors.Is(err, backend.ErrNotFound) {
		http.Error(w, "pooka: no environment named "+name, http.StatusNotFound)
		return
	}
	p.Log.Printf("proxy: %s: %v", name, err)
	http.Error(w, "pooka: "+err.Error(), http.StatusBadGateway)
}
