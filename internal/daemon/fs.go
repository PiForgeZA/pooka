package daemon

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/PiForgeZA/pooka/internal/backend"
	"github.com/PiForgeZA/pooka/internal/host"
)

const maxWriteBody = 16 << 20

// resolve joins a relative path to workingDir, as the Sprites API expects.
func resolve(p, workingDir string) string {
	if path.IsAbs(p) {
		return path.Clean(p)
	}
	if workingDir == "" {
		workingDir = "/home/pooka"
	}
	return path.Clean(path.Join(workingDir, p))
}

// fsExec runs a root shell snippet inside the environment and returns its output.
func (s *Server) fsExec(r *http.Request, script string, args []string, stdin *bytes.Reader) (stdout, stderr string, code int, err error) {
	var out, errb bytes.Buffer
	cmd := append([]string{"sh", "-c", script, "sh"}, args...)
	opts := backend.ExecOptions{Stdout: &out, Stderr: &errb, Root: true}
	if stdin != nil {
		opts.Stdin = stdin
	}
	code, err = s.Backend.Exec(r.Context(), r.PathValue("name"), cmd, opts)
	return out.String(), errb.String(), code, err
}

func (s *Server) fsRead(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	p := resolve(q.Get("path"), q.Get("workingDir"))
	if q.Get("path") == "" {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}
	var out, errb bytes.Buffer
	code, err := s.Backend.Exec(r.Context(), r.PathValue("name"), []string{"cat", "--", p}, backend.ExecOptions{Stdout: &out, Stderr: &errb, Root: true})
	if err != nil {
		writeBackendError(w, err)
		return
	}
	if code != 0 {
		writeError(w, http.StatusNotFound, strings.TrimSpace(errb.String()))
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	_, _ = w.Write(out.Bytes())
}

func (s *Server) fsWrite(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("path") == "" {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}
	p := resolve(q.Get("path"), q.Get("workingDir"))
	mode := q.Get("mode")
	if mode == "" {
		mode = "0644"
	}
	if _, err := strconv.ParseUint(mode, 8, 32); err != nil {
		writeError(w, http.StatusBadRequest, "mode must be octal, for example 0644")
		return
	}
	mkdir := q.Get("mkdir") == "true" || q.Get("mkdirParents") == "true"
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxWriteBody))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, err.Error())
		return
	}
	script := `f="$1"; m="$2"; `
	if mkdir {
		script += `mkdir -p "$(dirname -- "$f")" && `
	}
	script += `cat > "$f" && chmod "$m" "$f" && stat -c %s "$f"`
	out, errb, code, err := s.fsExec(r, script, []string{p, mode}, bytes.NewReader(body))
	if err != nil {
		writeBackendError(w, err)
		return
	}
	if code != 0 {
		writeError(w, http.StatusInternalServerError, strings.TrimSpace(errb))
		return
	}
	size, _ := strconv.Atoi(strings.TrimSpace(out))
	writeJSON(w, http.StatusOK, map[string]any{"path": p, "size": size, "mode": mode})
}

type fsEntry struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Type    string `json:"type"`
	Size    int64  `json:"size"`
	Mode    string `json:"mode"`
	ModTime string `json:"modTime"`
	IsDir   bool   `json:"isDir"`
}

func (s *Server) fsList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("path") == "" {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}
	p := resolve(q.Get("path"), q.Get("workingDir"))
	// One line per entry: name, type letter, size, octal mode, mtime epoch.
	script := `test -d "$1" || { echo "not a directory: $1" >&2; exit 1; }; find "$1" -mindepth 1 -maxdepth 1 -printf '%f\t%y\t%s\t%#m\t%T@\n' | sort`
	out, errb, code, err := s.fsExec(r, script, []string{p}, nil)
	if err != nil {
		writeBackendError(w, err)
		return
	}
	if code != 0 {
		writeError(w, http.StatusNotFound, strings.TrimSpace(errb))
		return
	}
	entries := parseFind(p, out)
	writeJSON(w, http.StatusOK, map[string]any{"path": p, "entries": entries, "count": len(entries)})
}

func parseFind(dir, out string) []fsEntry {
	entries := []fsEntry{}
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.Split(l, "\t")
		if len(f) < 5 {
			continue
		}
		size, _ := strconv.ParseInt(f[2], 10, 64)
		typ := map[string]string{"f": "file", "d": "directory", "l": "symlink"}[f[1]]
		if typ == "" {
			typ = "other"
		}
		secs, _ := strconv.ParseFloat(f[4], 64)
		entries = append(entries, fsEntry{
			Name: f[0], Path: path.Join(dir, f[0]), Type: typ, Size: size, Mode: f[3],
			ModTime: time.Unix(int64(secs), 0).UTC().Format(time.RFC3339), IsDir: f[1] == "d",
		})
	}
	return entries
}

// owner is a uid or gid in a ChownRequest. The spec types it as an object; the
// SDKs send a number. pooka also accepts a user or group name.
type owner string

func (o *owner) UnmarshalJSON(b []byte) error {
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	switch t := v.(type) {
	case nil:
		*o = ""
	case float64:
		*o = owner(strconv.FormatInt(int64(t), 10))
	case string:
		*o = owner(t)
	default:
		return errors.New("uid and gid must be a number or a name")
	}
	return nil
}

var ownerRe = regexp.MustCompile(`^[a-z_][a-z0-9_-]*$|^[0-9]+$`)

func (s *Server) fsChown(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path       string `json:"path"`
		WorkingDir string `json:"workingDir"`
		UID        owner  `json:"uid"`
		GID        owner  `json:"gid"`
		Recursive  bool   `json:"recursive"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if req.Path == "" {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}
	if req.UID == "" && req.GID == "" {
		writeError(w, http.StatusBadRequest, "uid or gid is required")
		return
	}
	for _, o := range []owner{req.UID, req.GID} {
		if o != "" && !ownerRe.MatchString(string(o)) {
			writeError(w, http.StatusBadRequest, "invalid uid or gid: "+string(o))
			return
		}
	}
	p := resolve(req.Path, req.WorkingDir)
	args := []string{"chown"}
	if req.Recursive {
		args = append(args, "-R")
	}
	args = append(args, string(req.UID)+":"+string(req.GID), p)
	var errb bytes.Buffer
	code, err := s.Backend.Exec(r.Context(), r.PathValue("name"), args, backend.ExecOptions{Stdout: io.Discard, Stderr: &errb, Root: true})
	if err != nil {
		writeBackendError(w, err)
		return
	}
	if code != 0 {
		writeError(w, http.StatusNotFound, strings.TrimSpace(errb.String()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"affected": []map[string]string{{"path": p}}, "count": 1})
}

// quoteForTest exists so the shell snippets above can be unit tested for quoting.
var _ = host.ShellQuote
