//go:build integration

package lxc

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/PiForgeZA/pooka/internal/config"
	"github.com/PiForgeZA/pooka/internal/host"
	"github.com/creack/pty"
)

// Run with POOKA_INTEGRATION_CLI pointing to the built CLI and POOKA_CONFIG
// pointing to a config for a test daemon. The test creates its own environment.
func TestExecDisconnectIntegration(t *testing.T) {
	cli := os.Getenv("POOKA_INTEGRATION_CLI")
	if cli == "" {
		t.Skip("set POOKA_INTEGRATION_CLI and POOKA_CONFIG to run host tests")
	}
	cli, err := filepath.Abs(cli)
	if err != nil {
		t.Fatal(err)
	}
	target := os.Getenv("POOKA_INTEGRATION_HOST")
	if target == "" {
		target = "root@proxmox"
	}
	name := fmt.Sprintf("fix3-%d", time.Now().UnixNano())
	runCLI := func(args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		return exec.CommandContext(ctx, cli, args...).CombinedOutput()
	}
	if out, err := runCLI("create", name); err != nil {
		t.Fatalf("create: %v: %s", err, out)
	}
	t.Cleanup(func() {
		if out, err := runCLI("destroy", name, "--force"); err != nil {
			t.Errorf("destroy %s: %v: %s", name, err, out)
		}
	})
	h := host.NewSSH(target)
	b := New(h, Options{})
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	// Get refuses containers without the pooka tag. Add the test tag before exec.
	env, err := b.Get(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	id := strconv.Itoa(env.VMID)
	if _, err := b.run(ctx, "pct", "set", id, "--tags", "pooka;pooka-test"); err != nil {
		t.Fatal(err)
	}
	for _, transport := range []string{"daemon", "ssh"} {
		for _, terminal := range []bool{false, true} {
			for _, root := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/tty=%t/root=%t", transport, terminal, root), func(t *testing.T) {
					args := []string{"--transport", transport, "exec", name}
					if terminal {
						args = append(args, "-t")
					}
					if root {
						args = append(args, "--root")
					}
					args = append(args, "--", "sh", "-c", `trap '' HUP TERM; echo READY=$$; exec sleep 30`)
					cmd := exec.Command(cli, args...)
					checkDisconnect(t, cmd, b, ctx, id)
				})
			}
		}
	}

	if sprite := os.Getenv("POOKA_INTEGRATION_SPRITE"); sprite != "" {
		cfg, err := config.Load()
		if err != nil {
			t.Fatal(err)
		}
		token, err := cfg.DaemonToken()
		if err != nil {
			t.Fatal(err)
		}
		for _, mode := range []string{"--stdin", "--http-post", "--tty"} {
			t.Run("sprite/"+mode, func(t *testing.T) {
				cmd := exec.Command(sprite, "exec", "-s", name, mode, "--", "sh", "-c", `trap '' HUP TERM; echo READY=$$; exec sleep 30`)
				cmd.Env = append(os.Environ(), "SPRITES_API_URL="+cfg.DaemonURL, "SPRITE_TOKEN="+token, "SPRITE_ORG=local")
				checkDisconnect(t, cmd, b, ctx, id)
			})
		}
	}
	for _, transport := range []string{"daemon", "ssh"} {
		t.Run("terminal-controls/"+transport, func(t *testing.T) {
			cmd := exec.Command(cli, "--transport", transport, "exec", name, "-t", "--", "env", "PS1=POOKA_PROMPT", "bash", "--noprofile", "--norc", "-i")
			f, err := pty.Start(cmd)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			defer cmd.Process.Kill()
			prompt := make(chan struct{})
			output := make(chan []byte, 1)
			go func() {
				var out bytes.Buffer
				buf := make([]byte, 4096)
				signaled := false
				for {
					n, err := f.Read(buf)
					out.Write(buf[:n])
					if !signaled && bytes.Contains(out.Bytes(), []byte("POOKA_PROMPT")) {
						close(prompt)
						signaled = true
					}
					if err != nil {
						output <- out.Bytes()
						return
					}
				}
			}()
			select {
			case <-prompt:
			case <-time.After(10 * time.Second):
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				t.Fatal("shell prompt did not arrive")
			}
			_, _ = io.WriteString(f, "sleep 30\n")
			time.Sleep(200 * time.Millisecond)
			_, _ = io.WriteString(f, "\x03")
			time.Sleep(100 * time.Millisecond)
			_, _ = io.WriteString(f, "exit 0\n")
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("shell exit: %v", err)
				}
			case <-time.After(3 * time.Second):
				_ = cmd.Process.Kill()
				<-done
				t.Error("Ctrl-C did not end the foreground command")
			}
			f.Close()
			if out := <-output; bytes.Contains(out, []byte("no job control")) {
				t.Errorf("job control unavailable: %s", out)
			}
		})
	}

	// Closed stdin must not close the exec session. The command reads the complete
	// input and returns its own exit code.
	t.Run("stdin-and-exit", func(t *testing.T) {
		cmd := exec.Command(cli, "exec", name, "--", "sh", "-c", `cat; exit 7`)
		cmd.Stdin = strings.NewReader("input preserved\n")
		out, err := cmd.CombinedOutput()
		ee, ok := err.(*exec.ExitError)
		if !ok || ee.ExitCode() != 7 || string(out) != "input preserved\n" {
			t.Fatalf("output=%q err=%v", out, err)
		}
	})
}

func checkDisconnect(t *testing.T, cmd *exec.Cmd, b *Backend, ctx context.Context, id string) {
	t.Helper()
	// A real terminal lets the CLI and ssh enter their normal TTY paths.
	f, err := pty.Start(cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	ready := make(chan int, 1)
	output := make(chan string, 1)
	go func() {
		var seen strings.Builder
		defer func() { output <- seen.String() }()
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			seen.WriteString(line + "\n")
			// The official CLI can prefix output with terminal queries.
			if i := strings.LastIndex(line, "READY="); i >= 0 {
				pid, _ := strconv.Atoi(line[i+len("READY="):])
				ready <- pid
				return
			}
		}
		ready <- 0
	}()
	var pid int
	select {
	case pid = <-ready:
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		f.Close()
		t.Fatalf("command did not become ready: %s", <-output)
	}
	if pid <= 0 {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("command exited before readiness: %s", <-output)
	}
	started := time.Now()
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	for {
		state, err := b.run(ctx, attach(id, "ps", "-o", "stat=", "-p", strconv.Itoa(pid))...)
		// ps returns 1 when the PID no longer exists. Other failures must fail.
		if strings.TrimSpace(state) == "" && err != nil {
			if strings.Contains(err.Error(), "exit 1") {
				break
			}
			t.Fatal(err)
		}
		if strings.HasPrefix(strings.TrimSpace(state), "Z") {
			break
		}
		if time.Since(started) > time.Second {
			t.Fatalf("process %d survived one second: %q (%v)", pid, state, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("cleanup took %s", elapsed)
	} else {
		t.Logf("cleanup verified in %s", elapsed)
	}
}
