// Package scripts ships the host-side shell scripts inside the pooka binary, so
// `pooka setup` can copy them to the Proxmox host without a checkout or a release
// archive. See ADR 0007.
package scripts

import "embed"

// FS holds build-template.sh, install-daemon.sh, bench.sh and template/*.
//
//go:embed *.sh template/*
var FS embed.FS
