// Package lxc implements backend.Backend with Proxmox LXC containers driven by pct.
// All pct knowledge lives here. See SPEC.md sections 9 to 11 and ADR 0001, 0003.
package lxc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
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

type resource struct {
	VMID   int    `json:"vmid"`
	Name   string `json:"name"`
	Status string `json:"status"`
	Type   string `json:"type"`
	Tags   string `json:"tags"`
	Node   string `json:"node"`
}

// resources lists every guest in the cluster in one call.
func (b *Backend) resources(ctx context.Context) ([]resource, error) {
	out, err := b.run(ctx, "pvesh", "get", "/cluster/resources", "--type", "vm", "--output-format", "json")
	if err != nil {
		return nil, err
	}
	var rs []resource
	if err := json.Unmarshal([]byte(out), &rs); err != nil {
		return nil, fmt.Errorf("parse cluster resources: %w", err)
	}
	return rs, nil
}

func hasTag(tags, want string) bool {
	for _, t := range strings.Split(tags, ";") {
		if strings.TrimSpace(t) == want {
			return true
		}
	}
	return false
}

// managed returns pooka's containers. Status comes from `pct list`, because the
// cluster resources status lags several seconds behind start and stop.
func (b *Backend) managed(ctx context.Context) ([]resource, error) {
	rs, err := b.resources(ctx)
	if err != nil {
		return nil, err
	}
	var out []resource
	for _, r := range rs {
		if r.Type == "lxc" && hasTag(r.Tags, Tag) {
			out = append(out, r)
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	live, err := b.liveStatus(ctx)
	if err != nil {
		return nil, err
	}
	for i := range out {
		if s, ok := live[out[i].VMID]; ok {
			out[i].Status = s
		}
	}
	return out, nil
}

// liveStatus parses `pct list` into VMID -> status.
func (b *Backend) liveStatus(ctx context.Context) (map[int]string, error) {
	out, err := b.run(ctx, "pct", "list")
	if err != nil {
		return nil, err
	}
	return parsePctList(out), nil
}

func parsePctList(out string) map[int]string {
	m := map[int]string{}
	for i, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		if i == 0 || len(f) < 2 {
			continue
		}
		if id, err := strconv.Atoi(f[0]); err == nil {
			m[id] = f[1]
		}
	}
	return m
}

func (b *Backend) find(ctx context.Context, name string) (resource, error) {
	rs, err := b.resources(ctx)
	if err != nil {
		return resource{}, err
	}
	for _, r := range rs {
		if r.Type == "lxc" && hasTag(r.Tags, Tag) && r.Name == name {
			st, err := b.run(ctx, "pct", "status", strconv.Itoa(r.VMID))
			if err != nil {
				return resource{}, err
			}
			if _, s, ok := strings.Cut(strings.TrimSpace(st), " "); ok {
				r.Status = s
			}
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
	e := backend.Env{Name: r.Name, VMID: r.VMID, Node: r.Node, Status: r.Status}
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

// freeVMID returns the lowest unused VMID in the configured range.
func (b *Backend) freeVMID(rs []resource) (int, error) {
	used := map[int]bool{}
	for _, r := range rs {
		used[r.VMID] = true
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
	rs, err := b.resources(ctx)
	if err != nil {
		return backend.Env{}, err
	}
	for _, r := range rs {
		if r.Type == "lxc" && hasTag(r.Tags, Tag) && r.Name == name {
			return backend.Env{}, fmt.Errorf("%w: %s (VMID %d)", backend.ErrExists, name, r.VMID)
		}
	}
	tpl := strconv.Itoa(b.opts.Template)
	if _, err := b.run(ctx, "pct", "config", tpl); err != nil {
		return backend.Env{}, fmt.Errorf("template %s not found; build one with scripts/build-template.sh: %w", tpl, err)
	}
	vmid, err := b.freeVMID(rs)
	if err != nil {
		return backend.Env{}, err
	}
	id := strconv.Itoa(vmid)

	if _, err := b.run(ctx, "pct", "clone", tpl, id, "--hostname", name); err != nil {
		return backend.Env{}, err
	}
	desc, _ := json.Marshal(map[string]any{
		"pooka": b.opts.Version, "created": time.Now().UTC().Format(time.RFC3339), "template": b.opts.Template,
	})
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
	var args []string
	if o.Root {
		args = attach(strconv.Itoa(r.VMID), "sh", "-lc", inner)
	} else {
		args = attach(strconv.Itoa(r.VMID), "runuser", "-l", "pooka", "-c", inner)
	}
	return b.h.Run(ctx, host.Cmd{Args: args, Stdin: o.Stdin, Stdout: o.Stdout, Stderr: o.Stderr, TTY: o.TTY})
}

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
