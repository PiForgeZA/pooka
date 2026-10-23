// Package lxc implements backend.Backend with Proxmox LXC containers driven by pct.
// All pct knowledge lives here. See SPEC.md sections 9 to 11 and ADR 0001, 0003.
package lxc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/PiForgeZA/pooka/internal/backend"
	"github.com/PiForgeZA/pooka/internal/host"
)

// Tag marks containers that pooka manages. Commands refuse all others.
const Tag = "pooka"

// Options configure the backend from the user's config.
type Options struct {
	Template  int
	VMIDLow   int
	VMIDHigh  int
	Version   string
	ReadyWait time.Duration // how long Create waits for systemd; default 30 s
	Sleep     func(time.Duration)
}

// Backend is the LXC implementation.
type Backend struct {
	h    host.Host
	opts Options
}

// New returns an LXC backend on the given host.
func New(h host.Host, opts Options) *Backend {
	if opts.ReadyWait == 0 {
		opts.ReadyWait = 30 * time.Second
	}
	if opts.Sleep == nil {
		opts.Sleep = time.Sleep
	}
	return &Backend{h: h, opts: opts}
}

var _ backend.Backend = (*Backend)(nil)

// run executes a command on the host and returns stdout. A non-zero exit is an error
// that carries the full stderr (SPEC section 14).
func (b *Backend) run(ctx context.Context, args ...string) (string, error) {
	var out, errb bytes.Buffer
	code, err := b.h.Run(ctx, host.Cmd{Args: args, Stdout: &out, Stderr: &errb})
	if err != nil {
		return "", err
	}
	if code != 0 {
		return out.String(), fmt.Errorf("%s failed (exit %d): %s", strings.Join(args[:min(3, len(args))], " "), code, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

// resource is one container as pooka sees it.
type resource struct {
	VMID    int
	Name    string
	Status  string
	Tags    string
	Node    string
	Created time.Time
	Desc    string // the raw percent-encoded description line, for Update
	backend.Settings
}

func hasTag(tags, want string) bool {
	for _, t := range strings.Split(tags, ";") {
		if strings.TrimSpace(t) == want {
			return true
		}
	}
	return false
}

// confScript lists tags and description comments of every container config, then the
// node name. Proxmox stores the description as leading "#" lines, percent-encoded.
const confScript = "grep -H '^tags:\\|^#' /etc/pve/lxc/*.conf 2>/dev/null; hostname"

// inventory reads pooka's containers from sources that are authoritative and
// immediate: `pct list` for VMID, status and name, and the config files for tags and
// creation time. The cluster resources endpoint lags several seconds behind create,
// start and stop, which made fresh environments "not found".
func (b *Backend) inventory(ctx context.Context) ([]resource, error) {
	list, err := b.run(ctx, "pct", "list")
	if err != nil {
		return nil, err
	}
	conf, err := b.run(ctx, "sh", "-c", confScript)
	if err != nil {
		return nil, err
	}
	tags, metas, node := parseConfs(conf)
	var out []resource
	for _, r := range parsePctList(list) {
		r.Tags = tags[r.VMID]
		r.Created = metas[r.VMID].Created
		r.Settings = metas[r.VMID].Settings
		r.Desc = metas[r.VMID].Desc
		r.Node = node
		if hasTag(r.Tags, Tag) {
			out = append(out, r)
		}
	}
	return out, nil
}

// parsePctList reads `pct list` output. Columns are VMID, Status, an optional Lock,
// and Name last.
func parsePctList(out string) []resource {
	var rs []resource
	for i, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		if i == 0 || len(f) < 3 {
			continue
		}
		id, err := strconv.Atoi(f[0])
		if err != nil {
			continue
		}
		rs = append(rs, resource{VMID: id, Status: f[1], Name: f[len(f)-1]})
	}
	return rs
}

// parseConfs reads confScript output: "<file>:tags: a;b", "<file>:#<encoded>", and
// the node name on the last line.
func parseConfs(out string) (tags map[int]string, metas map[int]meta, node string) {
	tags, metas = map[int]string{}, map[int]meta{}
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		file, rest, ok := strings.Cut(l, ".conf:")
		if !ok {
			node = strings.TrimSpace(l)
			continue
		}
		vmid, err := strconv.Atoi(path.Base(file))
		if err != nil {
			continue
		}
		switch {
		case strings.HasPrefix(rest, "tags:"):
			tags[vmid] = strings.TrimSpace(strings.TrimPrefix(rest, "tags:"))
		case strings.HasPrefix(rest, "#"):
			if m, ok := metaFromDescription(rest[1:]); ok {
				metas[vmid] = m
			}
		}
	}
	return tags, metas, node
}

// usedVMIDs returns every VMID that has a container or VM config on this node.
// Config files exist the instant a guest is created, unlike cluster resources.
func (b *Backend) usedVMIDs(ctx context.Context) (map[int]bool, error) {
	out, err := b.run(ctx, "sh", "-c", "ls /etc/pve/lxc /etc/pve/qemu-server 2>/dev/null")
	if err != nil {
		return nil, err
	}
	used := map[int]bool{}
	for _, l := range strings.Fields(out) {
		if id, err := strconv.Atoi(strings.TrimSuffix(l, ".conf")); err == nil {
			used[id] = true
		}
	}
	return used, nil
}

func (b *Backend) managed(ctx context.Context) ([]resource, error) {
	return b.inventory(ctx)
}

func (b *Backend) find(ctx context.Context, name string) (resource, error) {
	rs, err := b.inventory(ctx)
	if err != nil {
		return resource{}, err
	}
	for _, r := range rs {
		if r.Name == name {
			return r, nil
		}
	}
	return resource{}, fmt.Errorf("%w: %s", backend.ErrNotFound, name)
}

func (b *Backend) ip(ctx context.Context, vmid int) string {
	out, err := b.run(ctx, "lxc-info", "-n", strconv.Itoa(vmid), "-iH")
	if err != nil {
		return ""
	}
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		if l != "" && !strings.Contains(l, ":") { // first IPv4
			return l
		}
	}
	return ""
}

func (b *Backend) toEnv(ctx context.Context, r resource) backend.Env {
	e := backend.Env{Name: r.Name, VMID: r.VMID, Node: r.Node, Status: r.Status, Created: r.Created, Settings: r.Settings}
	if r.Status == "running" {
		e.IP = b.ip(ctx, r.VMID)
	}
	return e
}

func (b *Backend) List(ctx context.Context) ([]backend.Env, error) {
	rs, err := b.managed(ctx)
	if err != nil {
		return nil, err
	}
	envs := make([]backend.Env, 0, len(rs))
	for _, r := range rs {
		envs = append(envs, b.toEnv(ctx, r))
	}
	return envs, nil
}

func (b *Backend) Get(ctx context.Context, name string) (backend.Env, error) {
	r, err := b.find(ctx, name)
	if err != nil {
		return backend.Env{}, err
	}
	return b.toEnv(ctx, r), nil
}

// meta is what pooka keeps in the container description: a JSON object with the
// pooka version, the creation time, the template, and the phase 3 settings
// (ADR 0008, 0009). Proxmox stores it percent-encoded on one line.
type meta struct {
	Created time.Time
	Desc    string // the raw line as the config file holds it
	backend.Settings
}

// Description JSON keys.
const (
	keyCreated     = "created"
	keyURLAuth     = "url_auth"
	keyIdleTimeout = "idle_timeout" // Go duration string, for example "30m0s"
)

// decodeDescription returns the JSON object in one percent-encoded description, or
// false when it is not pooka's.
func decodeDescription(d string) (map[string]any, bool) {
	if u, err := url.PathUnescape(d); err == nil {
		d = u
	}
	var m map[string]any
	if json.Unmarshal([]byte(strings.TrimSpace(d)), &m) != nil || m == nil {
		return nil, false
	}
	return m, true
}

// metaFromDescription decodes one percent-encoded description. ok is false when
// it is not pooka's JSON.
func metaFromDescription(d string) (m meta, ok bool) {
	obj, ok := decodeDescription(d)
	if !ok {
		return meta{}, false
	}
	m.Desc = d
	if s, _ := obj[keyCreated].(string); s != "" {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			m.Created = t
		}
	}
	if s, _ := obj[keyURLAuth].(string); backend.ValidateURLAuth(s) == nil {
		m.URLAuth = s
	}
	if s, _ := obj[keyIdleTimeout].(string); s != "" {
		if dur, err := time.ParseDuration(s); err == nil && dur >= 0 {
			m.IdleTimeout = &dur
		}
	}
	return m, true
}

// Update merges the settings into the description JSON and writes it back with
// `pct set`. Other keys in the JSON are kept. The inventory already read the
// description, so this costs one host call beyond the lookup.
func (b *Backend) Update(ctx context.Context, name string, s backend.Settings) error {
	if err := backend.ValidateURLAuth(s.URLAuth); err != nil {
		return err
	}
	r, err := b.find(ctx, name)
	if err != nil {
		return err
	}
	id := strconv.Itoa(r.VMID)
	obj, ok := decodeDescription(r.Desc)
	if !ok {
		obj = map[string]any{}
	}
	if s.URLAuth != "" {
		obj[keyURLAuth] = s.URLAuth
	}
	if s.IdleTimeout != nil {
		obj[keyIdleTimeout] = s.IdleTimeout.String()
	}
	desc, err := json.Marshal(obj)
	if err != nil {
		return err
	}
	_, err = b.run(ctx, "pct", "set", id, "--description", string(desc))
	return err
}

// freeVMID returns the lowest VMID in the configured range with no config file.
func (b *Backend) freeVMID(ctx context.Context) (int, error) {
	used, err := b.usedVMIDs(ctx)
	if err != nil {
		return 0, err
	}
	for id := b.opts.VMIDLow; id <= b.opts.VMIDHigh; id++ {
		if !used[id] {
			return id, nil
		}
	}
	return 0, fmt.Errorf("no free VMID in range %d-%d", b.opts.VMIDLow, b.opts.VMIDHigh)
}

func (b *Backend) Create(ctx context.Context, name string, o backend.CreateOptions) (backend.Env, error) {
	if err := backend.ValidateName(name); err != nil {
		return backend.Env{}, err
	}
	if r, err := b.find(ctx, name); err == nil {
		return backend.Env{}, fmt.Errorf("%w: %s (VMID %d)", backend.ErrExists, name, r.VMID)
	} else if !errors.Is(err, backend.ErrNotFound) {
		return backend.Env{}, err
	}
	tpl := strconv.Itoa(b.opts.Template)
	if _, err := b.run(ctx, "pct", "config", tpl); err != nil {
		return backend.Env{}, fmt.Errorf("template %s not found; build one with scripts/build-template.sh: %w", tpl, err)
	}
	vmid, err := b.freeVMID(ctx)
	if err != nil {
		return backend.Env{}, err
	}
	id := strconv.Itoa(vmid)

	if _, err := b.run(ctx, "pct", "clone", tpl, id, "--hostname", name); err != nil {
		return backend.Env{}, err
	}
	meta := map[string]any{
		"pooka": b.opts.Version, keyCreated: time.Now().UTC().Format(time.RFC3339), "template": b.opts.Template,
	}
	if o.IdleTimeout != nil {
		meta[keyIdleTimeout] = o.IdleTimeout.String()
	}
	desc, _ := json.Marshal(meta)
	setArgs := []string{"pct", "set", id, "--tags", Tag, "--description", string(desc)}
	if o.Cores > 0 {
		setArgs = append(setArgs, "--cores", strconv.Itoa(o.Cores))
	}
	if o.MemoryMB > 0 {
		setArgs = append(setArgs, "--memory", strconv.Itoa(o.MemoryMB))
	}
	if _, err := b.run(ctx, setArgs...); err != nil {
		return backend.Env{}, err
	}
	if o.DiskGB > 0 {
		if cur := b.rootfsSizeGB(ctx, id); cur > 0 && cur < o.DiskGB {
			if _, err := b.run(ctx, "pct", "resize", id, "rootfs", fmt.Sprintf("%dG", o.DiskGB)); err != nil {
				return backend.Env{}, err
			}
		}
	}
	if _, err := b.run(ctx, "pct", "start", id); err != nil {
		return backend.Env{}, err
	}
	if err := b.waitReady(ctx, id); err != nil {
		return backend.Env{}, err
	}
	if _, err := b.run(ctx, attach(id, "test", "-x", "/etc/pooka/firstboot.sh")...); err == nil {
		env := []string{"POOKA_NAME=" + name}
		if o.SSHPubKey != "" {
			env = append(env, "POOKA_SSH_PUBKEY="+o.SSHPubKey)
		}
		args := attach(id, append([]string{"env"}, env...)...)
		args = append(args, "/etc/pooka/firstboot.sh")
		if _, err := b.run(ctx, args...); err != nil {
			return backend.Env{}, fmt.Errorf("firstboot: %w", err)
		}
	}
	return backend.Env{Name: name, VMID: vmid, Status: "running", IP: b.ip(ctx, vmid)}, nil
}

// rootfsSizeGB parses "rootfs: local-lvm:vm-9100-disk-0,size=16G". Returns 0 if unknown.
func (b *Backend) rootfsSizeGB(ctx context.Context, id string) int {
	out, err := b.run(ctx, "pct", "config", id)
	if err != nil {
		return 0
	}
	_, size := parseRootfs(out)
	return size
}

// parseRootfs returns the rootfs volume ID and its size in GB from pct config output.
func parseRootfs(config string) (volid string, sizeGB int) {
	for _, l := range strings.Split(config, "\n") {
		if !strings.HasPrefix(l, "rootfs:") {
			continue
		}
		for i, part := range strings.Split(strings.TrimSpace(strings.TrimPrefix(l, "rootfs:")), ",") {
			if i == 0 {
				volid = part
				continue
			}
			if v, ok := strings.CutPrefix(part, "size="); ok {
				v = strings.TrimSuffix(v, "G")
				sizeGB, _ = strconv.Atoi(v)
			}
		}
	}
	return volid, sizeGB
}

func (b *Backend) waitReady(ctx context.Context, id string) error {
	deadline := time.Now().Add(b.opts.ReadyWait)
	for {
		var out bytes.Buffer
		_, err := b.h.Run(ctx, host.Cmd{Args: attach(id, "systemctl", "is-system-running"), Stdout: &out})
		if err != nil {
			return err
		}
		switch strings.TrimSpace(out.String()) {
		case "running", "degraded":
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("container %s did not become ready within %s", id, b.opts.ReadyWait)
		}
		b.opts.Sleep(200 * time.Millisecond)
	}
}

func (b *Backend) Start(ctx context.Context, name string) error {
	r, err := b.find(ctx, name)
	if err != nil {
		return err
	}
	if r.Status == "running" {
		return nil
	}
	_, err = b.run(ctx, "pct", "start", strconv.Itoa(r.VMID))
	return err
}

func (b *Backend) Stop(ctx context.Context, name string) error {
	r, err := b.find(ctx, name)
	if err != nil {
		return err
	}
	if r.Status != "running" {
		return nil
	}
	return b.stopVMID(ctx, r.VMID)
}

// stopVMID shuts down cleanly and forces a stop after 10 s (SPEC section 10).
func (b *Backend) stopVMID(ctx context.Context, vmid int) error {
	_, err := b.run(ctx, "pct", "shutdown", strconv.Itoa(vmid), "--timeout", "10", "--forceStop", "1")
	return err
}

func (b *Backend) Destroy(ctx context.Context, name string) error {
	r, err := b.find(ctx, name)
	if err != nil {
		return err
	}
	id := strconv.Itoa(r.VMID)
	if r.Status == "running" {
		if _, err := b.run(ctx, "pct", "stop", id); err != nil {
			return err
		}
	}
	_, err = b.run(ctx, "pct", "destroy", id, "--purge")
	return err
}

func (b *Backend) Exec(ctx context.Context, name string, cmd []string, o backend.ExecOptions) (int, error) {
	r, err := b.find(ctx, name)
	if err != nil {
		return -1, err
	}
	if r.Status != "running" {
		return -1, fmt.Errorf("%s is %s; run `pooka start %s`", name, r.Status, name)
	}
	inner := host.ShellJoin(cmd)
	if o.Dir != "" {
		inner = "cd " + host.ShellQuote(o.Dir) + " && " + inner
	}
	for i := len(o.Env) - 1; i >= 0; i-- {
		k, v, ok := strings.Cut(o.Env[i], "=")
		if !ok || !envKeyRe.MatchString(k) {
			continue
		}
		inner = "export " + k + "=" + host.ShellQuote(v) + "; " + inner
	}
	var args []string
	switch {
	case o.Root:
		args = attach(strconv.Itoa(r.VMID), "sh", "-lc", inner)
	default:
		// runuser -c starts a new session, which drops the controlling terminal:
		// bash then warns "no job control in this shell" and Ctrl-C does not reach
		// the foreground job. --session-command keeps the session that lxc-attach
		// gave its pty to. The pty is private to this exec, so the tty-hijack
		// concern behind "-c" does not apply. Without a TTY, keep the host
		// command's process group so cancellation also reaches its children.
		args = attach(strconv.Itoa(r.VMID), "runuser", "-l", "pooka", "--session-command", inner)
	}
	args = superviseExec(args, o.TTY || o.PTY != nil)
	return b.h.Run(ctx, host.Cmd{Args: args, Stdin: o.Stdin, Stdout: o.Stdout, Stderr: o.Stderr, TTY: o.TTY, PTY: o.PTY, DedicatedSession: true})
}

var envKeyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// attach builds an lxc-attach command. pct exec wraps the same call but pays about
// 0.5 s of perl startup per invocation; lxc-attach takes under 10 ms.
func attach(id string, cmd ...string) []string {
	return append([]string{"lxc-attach", "-n", id, "--clear-env", "--keep-var", "TERM", "--"}, cmd...)
}

func tmpName(local string) string {
	return fmt.Sprintf("/tmp/pooka-%d-%s", time.Now().UnixNano(), path.Base(local))
}

func (b *Backend) Push(ctx context.Context, name, local, remote string) error {
	r, err := b.find(ctx, name)
	if err != nil {
		return err
	}
	if _, err := os.Stat(local); err != nil {
		return err
	}
	tmp := tmpName(local)
	if err := b.h.CopyTo(ctx, local, tmp); err != nil {
		return err
	}
	defer b.run(ctx, "rm", "-f", tmp) //nolint:errcheck
	_, err = b.run(ctx, "pct", "push", strconv.Itoa(r.VMID), tmp, remote, "--user", "pooka", "--group", "pooka")
	return err
}

func (b *Backend) Pull(ctx context.Context, name, remote, local string) error {
	r, err := b.find(ctx, name)
	if err != nil {
		return err
	}
	tmp := tmpName(remote)
	if _, err := b.run(ctx, "pct", "pull", strconv.Itoa(r.VMID), remote, tmp); err != nil {
		return err
	}
	defer b.run(ctx, "rm", "-f", tmp) //nolint:errcheck
	return b.h.CopyFrom(ctx, tmp, local)
}

func (b *Backend) Checkpoint(ctx context.Context, name, comment string) (backend.Checkpoint, error) {
	r, err := b.find(ctx, name)
	if err != nil {
		return backend.Checkpoint{}, err
	}
	now := time.Now()
	id := "cp-" + strconv.FormatInt(now.Unix(), 10)
	args := []string{"pct", "snapshot", strconv.Itoa(r.VMID), id}
	if comment != "" {
		args = append(args, "--description", comment)
	}
	if _, err := b.run(ctx, args...); err != nil {
		return backend.Checkpoint{}, err
	}
	return backend.Checkpoint{ID: id, Time: now, Comment: comment}, nil
}

type snapshot struct {
	Name        string `json:"name"`
	SnapTime    int64  `json:"snaptime"`
	Description string `json:"description"`
}

func (b *Backend) ListCheckpoints(ctx context.Context, name string) ([]backend.Checkpoint, error) {
	r, err := b.find(ctx, name)
	if err != nil {
		return nil, err
	}
	out, err := b.run(ctx, "pvesh", "get", fmt.Sprintf("/nodes/%s/lxc/%d/snapshot", r.Node, r.VMID), "--output-format", "json")
	if err != nil {
		return nil, err
	}
	var snaps []snapshot
	if err := json.Unmarshal([]byte(out), &snaps); err != nil {
		return nil, fmt.Errorf("parse snapshots: %w", err)
	}
	var cps []backend.Checkpoint
	for _, s := range snaps {
		if s.Name == "current" {
			continue
		}
		cps = append(cps, backend.Checkpoint{ID: s.Name, Time: time.Unix(s.SnapTime, 0), Comment: strings.TrimSpace(s.Description)})
	}
	// pvesh returns the snapshots in hash order. Sort oldest first, then by id.
	sort.Slice(cps, func(i, j int) bool {
		if !cps[i].Time.Equal(cps[j].Time) {
			return cps[i].Time.Before(cps[j].Time)
		}
		return cps[i].ID < cps[j].ID
	})
	return cps, nil
}

func (b *Backend) DeleteCheckpoint(ctx context.Context, name, id string) error {
	r, err := b.find(ctx, name)
	if err != nil {
		return err
	}
	_, err = b.run(ctx, "pct", "delsnapshot", strconv.Itoa(r.VMID), id)
	return err
}

// Restore is stop, rollback, clear MMP on LVM-thin (ADR 0003), start.
func (b *Backend) Restore(ctx context.Context, name, id string) error {
	r, err := b.find(ctx, name)
	if err != nil {
		return err
	}
	vm := strconv.Itoa(r.VMID)
	if r.Status == "running" {
		if _, err := b.run(ctx, "pct", "stop", vm); err != nil {
			return err
		}
	}
	if _, err := b.run(ctx, "pct", "rollback", vm, id); err != nil {
		return err
	}
	if err := b.clearMMP(ctx, vm); err != nil {
		return err
	}
	_, err = b.run(ctx, "pct", "start", vm)
	return err
}

// clearMMP clears the ext4 multi-mount-protection block on the root volume when the
// storage is LVM-thin. Without this the next mount waits about 43 s. See ADR 0003.
func (b *Backend) clearMMP(ctx context.Context, vm string) error {
	cfg, err := b.run(ctx, "pct", "config", vm)
	if err != nil {
		return err
	}
	volid, _ := parseRootfs(cfg)
	storage, _, ok := strings.Cut(volid, ":")
	if !ok {
		return nil
	}
	out, err := b.run(ctx, "pvesh", "get", "/storage/"+storage, "--output-format", "json")
	if err != nil {
		return err
	}
	var st struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		return fmt.Errorf("parse storage %s: %w", storage, err)
	}
	if st.Type != "lvmthin" {
		return nil
	}
	dev, err := b.run(ctx, "pvesm", "path", volid)
	if err != nil {
		return err
	}
	_, err = b.run(ctx, "tune2fs", "-f", "-E", "clear_mmp", strings.TrimSpace(dev))
	return err
}
