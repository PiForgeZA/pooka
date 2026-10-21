package host

import (
	"os/exec"
	"syscall"
)

// Close the dedicated SSH connection even if the Linux client is killed without
// running its context cancellation handler.
func killWithParent(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
}
