//go:build !linux

package host

import "os/exec"

// Other platforms close the connection through context cancellation.
func killWithParent(c *exec.Cmd) {}
