// Package ptrbox embeds the guest-side assets: the Lima VM templates, the
// provisioning scripts that run inside them, the verification script, and the
// squid config template plus its seed allowlist.
//
// These stay as .yaml/.sh/.txt files rather than becoming Go string constants
// for two reasons. They execute inside Debian guests, so bash is their native
// language and rewriting them in Go is not on the table. And vm/ is the
// documented source of truth for what a sandbox VM contains - the review
// surface for every security-relevant change - which only works if the files
// are readable on their own terms.
//
// Embedding them means the compiled binary carries everything it provisions:
// no PTRBOX_ROOT, no checkout to locate, nothing on disk that a later run
// could find modified. `go install` produces a complete ptrbox.
package ptrbox

import "embed"

// Assets holds the vm/ and host/ trees, rooted exactly as they appear in the
// repository, so paths like "vm/provision/10-base.sh" and
// "host/squid.conf.in" mean the same thing in code, in tests and in docs.
//
//go:embed vm host
var Assets embed.FS
