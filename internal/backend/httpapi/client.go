// Package httpapi is a Backend that talks to pookad over the Sprites HTTP API, plus
// the pooka extensions under /pooka/v1. See ADR 0005 and docs/sprites-api/README.md.
package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/PiForgeZA/pooka/internal/backend"
)

// Client implements backend.Backend against one pookad.
type Client struct {
	URL       string // base URL, for example http://proxmox:7700
	Token     string
	HTTP      *http.Client
	UserAgent string
	// Progress receives the info lines of streamed operations (checkpoint, restore).
	// nil discards them.
	Progress io.Writer
	// Debug, when set, receives one line per request with its duration.
	Debug io.Writer
	// term drives TTY exec. nil means the process's own terminal.
	term terminal
}

// New returns a client for base URL u with a bearer token.
func New(u, token string) *Client {
	return &Client{URL: strings.TrimRight(u, "/"), Token: token, HTTP: &http.Client{}, UserAgent: "pooka"}
}

// --- transport helpers ---

func (c *Client) headers() http.Header {
	return http.Header{"Authorization": {"Bearer " + c.Token}, "User-Agent": {c.UserAgent}}
}

func (c *Client) newRequest(ctx context.Context, method, p string, q url.Values, body io.Reader) (*http.Request, error) {
	u := c.URL + p
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	req.Header = c.headers()
	return req, nil
}

// do sends a request and returns the response if its status is below 400. Other
// statuses become errors; the body is closed. JSON bodies are encoded from v.
func (c *Client) do(ctx context.Context, method, p string, q url.Values, v any) (*http.Response, error) {
	var body io.Reader
	var ctype string
	switch b := v.(type) {
	case nil:
	case io.Reader:
		body = b
		ctype = "application/octet-stream"
	default:
		buf, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(buf)
		ctype = "application/json"
	}
	req, err := c.newRequest(ctx, method, p, q, body)
	if err != nil {
		return nil, err
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	start := time.Now()
	resp, err := c.HTTP.Do(req)
	if c.Debug != nil {
		status := "error"
		if resp != nil {
			status = strconv.Itoa(resp.StatusCode)
		}
		fmt.Fprintf(c.Debug, "[daemon %6.2fs %s] %s %s\n", time.Since(start).Seconds(), status, method, p)
	}
	if err != nil {
		return nil, c.reachError(err)
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		return nil, c.statusError(resp)
	}
	return resp, nil
}

// doJSON runs do and decodes the JSON response into out (may be nil).
func (c *Client) doJSON(ctx context.Context, method, p string, q url.Values, in, out any) error {
	resp, err := c.do(ctx, method, p, q, in)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) reachError(err error) error {
	return fmt.Errorf("cannot reach pookad at %s: %w", c.URL, err)
}

// serverError keeps the server's message verbatim and still matches the backend
// sentinel the status code stands for, so errors.Is works without a doubled message.
type serverError struct {
	msg string
	is  error
}

func (e serverError) Error() string        { return e.msg }
func (e serverError) Is(target error) bool { return target == e.is }

func notFoundError(msg string) error { return serverError{msg: msg, is: backend.ErrNotFound} }

// statusError maps an HTTP error response to a backend error. SPEC section 14: show
// the server's message verbatim.
func (c *Client) statusError(resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	msg := strings.TrimSpace(string(b))
	var je struct {
		Error string `json:"error"`
	}
	isJSON := json.Unmarshal(b, &je) == nil && je.Error != ""
	if isJSON {
		msg = je.Error
	}
	if msg == "" {
		msg = http.StatusText(resp.StatusCode)
	}
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return fmt.Errorf("pookad rejected the token (%s): set token or token_file in the pooka config, or SPRITE_TOKEN", msg)
	case http.StatusNotFound:
		if !isJSON {
			// Go's mux answers plain text when the route does not exist at all:
			// an older pookad without this endpoint.
			return notFoundError(fmt.Sprintf("%s %s: this pookad has no such endpoint; upgrade it with scripts/install-daemon.sh", resp.Request.Method, resp.Request.URL.Path))
		}
		return notFoundError(msg)
	case http.StatusConflict:
		return serverError{msg: msg, is: backend.ErrExists}
	case http.StatusRequestEntityTooLarge:
		return fmt.Errorf("%s: pookad accepts files up to 16 MB; use --transport ssh for larger files", msg)
	case http.StatusNotImplemented:
		return fmt.Errorf("%s (upgrade pookad: run scripts/install-daemon.sh with the current build)", msg)
	default:
		return fmt.Errorf("pookad: %s (HTTP %d)", msg, resp.StatusCode)
	}
}

// --- Sprites shapes ---

type sprite struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Status      string `json:"status"`
	URL         string `json:"url"`
	CreatedAt   string `json:"created_at"`
	URLSettings *struct {
		Auth string `json:"auth"`
	} `json:"url_settings"`
	// Pooka is the daemon's extension object (ADR 0005, 0008).
	Pooka *struct {
		IP          string  `json:"ip"`
		IdleTimeout *string `json:"idle_timeout"`
	} `json:"pooka"`
}

func toEnv(sp sprite) backend.Env {
	e := backend.Env{Name: sp.Name, Status: "stopped"}
	if sp.Status == "running" {
		e.Status = "running"
	}
	if n, err := strconv.Atoi(strings.TrimPrefix(sp.ID, "sprite-")); err == nil {
		e.VMID = n
	}
	if sp.Pooka != nil {
		e.IP = sp.Pooka.IP
		if sp.Pooka.IdleTimeout != nil {
			if d, err := time.ParseDuration(*sp.Pooka.IdleTimeout); err == nil {
				e.IdleTimeout = &d
			}
		}
	}
	if u, err := url.Parse(sp.URL); err == nil && u.Host != "" {
		if u.Scheme == "https" {
			e.URL = sp.URL
		} else if e.IP == "" {
			e.IP = u.Hostname() // a phase 2 daemon: url is http://<ip>:8080
		}
	}
	if sp.URLSettings != nil {
		e.URLAuth = sp.URLSettings.Auth
	}
	if t, err := time.Parse(time.RFC3339, sp.CreatedAt); err == nil {
		e.Created = t
	}
	return e
}

type checkpointInfo struct {
	ID         string `json:"id"`
	CreateTime string `json:"create_time"`
	Comment    string `json:"comment"`
}

func toCheckpoint(ci checkpointInfo) backend.Checkpoint {
	cp := backend.Checkpoint{ID: ci.ID, Comment: ci.Comment}
	if t, err := time.Parse(time.RFC3339, ci.CreateTime); err == nil {
		cp.Time = t
	}
	return cp
}

func spritePath(name string, rest ...string) string {
	return path.Join(append([]string{"/v1/sprites", url.PathEscape(name)}, rest...)...)
}

func pookaPath(name string, rest ...string) string {
	return path.Join(append([]string{"/pooka/v1/sprites", url.PathEscape(name)}, rest...)...)
}

// --- Backend ---

func (c *Client) Create(ctx context.Context, name string, o backend.CreateOptions) (backend.Env, error) {
	if err := backend.ValidateName(name); err != nil {
		return backend.Env{}, err
	}
	req := map[string]any{"name": name, "cores": o.Cores, "memory_mb": o.MemoryMB, "disk_gb": o.DiskGB, "ssh_pubkey": o.SSHPubKey}
	if o.IdleTimeout != nil {
		req["idle_timeout"] = o.IdleTimeout.String()
	}
	var sp sprite
	if err := c.doJSON(ctx, http.MethodPost, "/pooka/v1/sprites", nil, req, &sp); err != nil {
		return backend.Env{}, err
	}
	return toEnv(sp), nil
}

// Update writes url_auth and idle_timeout through the pooka settings extension.
func (c *Client) Update(ctx context.Context, name string, s backend.Settings) error {
	if err := backend.ValidateURLAuth(s.URLAuth); err != nil {
		return err
	}
	req := map[string]any{"url_auth": s.URLAuth}
	if s.IdleTimeout != nil {
		req["idle_timeout"] = s.IdleTimeout.String()
	}
	return c.doJSON(ctx, http.MethodPut, pookaPath(name, "settings"), nil, req, nil)
}

func (c *Client) List(ctx context.Context) ([]backend.Env, error) {
	var resp struct {
		Sprites []sprite `json:"sprites"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/v1/sprites", url.Values{"max_results": {"100"}}, nil, &resp); err != nil {
		return nil, err
	}
	out := make([]backend.Env, 0, len(resp.Sprites))
	for _, sp := range resp.Sprites {
		out = append(out, toEnv(sp))
	}
	return out, nil
}

func (c *Client) Get(ctx context.Context, name string) (backend.Env, error) {
	var sp sprite
	if err := c.doJSON(ctx, http.MethodGet, spritePath(name), nil, nil, &sp); err != nil {
		return backend.Env{}, err
	}
	return toEnv(sp), nil
}

func (c *Client) Start(ctx context.Context, name string) error {
	return c.doJSON(ctx, http.MethodPost, pookaPath(name, "start"), nil, nil, nil)
}

func (c *Client) Stop(ctx context.Context, name string) error {
	return c.doJSON(ctx, http.MethodPost, pookaPath(name, "stop"), nil, nil, nil)
}

func (c *Client) Destroy(ctx context.Context, name string) error {
	return c.doJSON(ctx, http.MethodDelete, spritePath(name), nil, nil, nil)
}

// Push writes the file with fs/write, then gives it to the pooka user with fs/chown,
// as `pct push --user pooka --group pooka` does over SSH.
func (c *Client) Push(ctx context.Context, name, local, remote string) error {
	f, err := os.Open(local)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if st.IsDir() {
		return fmt.Errorf("push %s: is a directory", local)
	}
	q := url.Values{"path": {remote}, "mode": {fmt.Sprintf("%04o", st.Mode().Perm())}}
	if err := c.doJSON(ctx, http.MethodPut, spritePath(name, "fs/write"), q, f, nil); err != nil {
		return err
	}
	chown := map[string]any{"path": remote, "uid": "pooka", "gid": "pooka", "recursive": false, "asRoot": true}
	return c.doJSON(ctx, http.MethodPost, spritePath(name, "fs/chown"), nil, chown, nil)
}

func (c *Client) Pull(ctx context.Context, name, remote, local string) error {
	resp, err := c.do(ctx, http.MethodGet, spritePath(name, "fs/read"), url.Values{"path": {remote}}, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	out, err := os.OpenFile(local, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, resp.Body); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// stream consumes an ndjson operation stream. Info lines go to Progress. It returns
// nil on "complete" and the server's message on "error".
func (c *Client) stream(ctx context.Context, p string, body any) error {
	resp, err := c.do(ctx, http.MethodPost, p, nil, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	dec := json.NewDecoder(resp.Body)
	for {
		var ev struct {
			Type  string `json:"type"`
			Data  string `json:"data"`
			Error string `json:"error"`
		}
		if err := dec.Decode(&ev); err != nil {
			if errors.Is(err, io.EOF) {
				return errors.New("pookad closed the stream before the operation finished")
			}
			return c.reachError(err)
		}
		switch ev.Type {
		case "info":
			if c.Progress != nil {
				fmt.Fprintln(c.Progress, ev.Data)
			}
		case "complete":
			return nil
		case "error":
			return errors.New(ev.Error)
		}
	}
}

// Checkpoint streams the create, then returns the checkpoint with the latest
// creation time from the list. The Sprites stream carries no id field (ADR 0005).
func (c *Client) Checkpoint(ctx context.Context, name, comment string) (backend.Checkpoint, error) {
	if err := c.stream(ctx, spritePath(name, "checkpoint"), map[string]string{"comment": comment}); err != nil {
		return backend.Checkpoint{}, err
	}
	cps, err := c.ListCheckpoints(ctx, name)
	if err != nil {
		return backend.Checkpoint{}, err
	}
	if len(cps) == 0 {
		return backend.Checkpoint{}, errors.New("checkpoint reported complete but the list is empty")
	}
	newest := cps[0]
	for _, cp := range cps[1:] {
		if !cp.Time.Before(newest.Time) {
			newest = cp
		}
	}
	return newest, nil
}

// ListCheckpoints returns oldest first, as the LXC backend does. The daemon sends
// newest first, as Sprites does.
func (c *Client) ListCheckpoints(ctx context.Context, name string) ([]backend.Checkpoint, error) {
	var list []checkpointInfo
	if err := c.doJSON(ctx, http.MethodGet, spritePath(name, "checkpoints"), nil, nil, &list); err != nil {
		return nil, err
	}
	out := make([]backend.Checkpoint, 0, len(list))
	for i := len(list) - 1; i >= 0; i-- {
		out = append(out, toCheckpoint(list[i]))
	}
	return out, nil
}

func (c *Client) DeleteCheckpoint(ctx context.Context, name, id string) error {
	return c.doJSON(ctx, http.MethodDelete, pookaPath(name, "checkpoints", url.PathEscape(id)), nil, nil, nil)
}

func (c *Client) Restore(ctx context.Context, name, id string) error {
	return c.stream(ctx, spritePath(name, "checkpoints", url.PathEscape(id), "restore"), nil)
}
