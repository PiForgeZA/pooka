package lxc

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/PiForgeZA/pooka/internal/host"
)

func TestExecSupervisorEndsSessionAfterParentDeath(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux parent-death signals")
	}
	for _, ignoredHUP := range []bool{false, true} {
		t.Run(fmt.Sprintf("ignoredHUP=%t", ignoredHUP), func(t *testing.T) {
			// Model lxc-attach's proxy process. Its child starts a separate session.
			proxy := filepath.Join(t.TempDir(), "attach")
			if err := os.WriteFile(proxy, []byte("#!/bin/sh\nwhile [ \"$1\" != -- ]; do shift; done\nshift\nexec 3<&0\n\"$@\" <&3 &\nwait\n"), 0700); err != nil {
				t.Fatal(err)
			}
			args := superviseExec(attach("test", "sh", "-c", `trap '' HUP TERM; echo $$; exec sleep 30`), false)
			args[3] = proxy
			if ignoredHUP {
				args = append([]string{"nohup"}, args...)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			reader, writer := io.Pipe()
			defer reader.Close()
			defer writer.Close()
			done := make(chan struct{})
			go func() {
				defer close(done)
				defer writer.Close()
				host.Local{}.Run(ctx, host.Cmd{Args: args, Stdout: writer, Stderr: io.Discard})
			}()
			line, err := bufio.NewReader(reader).ReadString('\n')
			if err != nil {
				t.Fatal(err)
			}
			pid, err := strconv.Atoi(strings.TrimSpace(line))
			if err != nil || pid <= 0 {
				t.Fatalf("child pid %q: %v", line, err)
			}
			defer syscall.Kill(pid, syscall.SIGKILL)
			cancel()
			deadline := time.Now().Add(time.Second)
			for {
				b, _ := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
				if len(b) == 0 || strings.Contains(string(b), ") Z ") {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("command survived its proxy for one second")
				}
				time.Sleep(10 * time.Millisecond)
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("Run did not return")
			}
		})
	}
}
