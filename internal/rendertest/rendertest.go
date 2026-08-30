// Package rendertest holds the fixed template values used by the golden-file
// test and the security invariant assertions, plus the rendering helpers both
// of them need.
//
// The values are deliberately not the real defaults: obviously fake host paths
// and identity keep personal data out of the checked-in golden file, and make
// an accidental "regenerated on my machine" diff obvious.
//
// It is a package of its own, rather than a _test.go file, because two test
// packages share it and Go does not let test files cross package boundaries.
// Nothing outside tests imports it, so it never reaches the binary.
package rendertest

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	ptrbox "github.com/PtrMzk/ptrbox"
	"github.com/PtrMzk/ptrbox/internal/render"
)

// Args are the sandbox VM template's values.
func Args() render.Values {
	return render.Values{
		"REPO_DIR":       "/Users/example/code/demo",
		"VM_NAME":        "demo",
		"VM_COLOR":       "1;32",
		"IMAGE_URL":      "https://cloud.debian.org/images/cloud/trixie/latest/debian-13-genericcloud-arm64.qcow2",
		"CPUS":           "4",
		"MEMORY":         "8GiB",
		"DISK":           "50GiB",
		"PORT_MIN":       "3000",
		"PORT_MAX":       "9000",
		"DNS_NFT_SET":    "9.9.9.9, 1.1.1.1",
		"DNS_LIST":       "9.9.9.9 1.1.1.1",
		"EXTRA_PACKAGES": "",
		"TOOLCHAIN":      "node uv",
		"NODE_VERSION":   "lts",
		"PLAYWRIGHT":     "true",
		"PROXY_HOST": "192.168.5.2",
		// The VM's own allocated port, not the base port: since item 37 every
		// sandbox dials its own, and 8889 is the first allocation.
		"PROXY_PORT":     "8889",
		"GIT_USER_NAME":  "Example Dev",
		"GIT_USER_EMAIL": "dev@example.com",
		"CLAUDE_MODEL":   "opus",
	}
}

// ProxyArgs are the egress proxy VM template's values.
func ProxyArgs() render.Values {
	return render.Values{
		"IMAGE_URL":        "https://cloud.debian.org/images/cloud/trixie/latest/debian-13-genericcloud-arm64.qcow2",
		"PROXY_CPUS":       "1",
		"PROXY_MEMORY":     "512MiB",
		"PROXY_DISK":       "4GiB",
		"PROXY_PORT":       "8888",
		"SANDBOX_PORT_MIN": "8889",
		"SANDBOX_PORT_MAX": "8904",
	}
}

// SquidArgs are the squid config template's values. The port block mirrors
// what internal/proxy generates for the default config; the proxy tests
// assert the real generator, this pins the template around it.
func SquidArgs() render.Values {
	ports := make([]string, 0, 16)
	for port := 8889; port <= 8904; port++ {
		ports = append(ports, fmt.Sprintf("http_port %d", port))
	}
	return render.Values{
		"PROXY_PORT":         "8888",
		"SANDBOX_HTTP_PORTS": strings.Join(ports, "\n"),
	}
}

// SquidConf renders host/squid.conf.in with SquidArgs.
func SquidConf(t *testing.T) string {
	return mustRender(t, "host/squid.conf.in", "host", SquidArgs())
}

// Sandbox renders vm/claude-repo.yaml with Args.
func Sandbox(t *testing.T) string { return mustRender(t, "vm/claude-repo.yaml", "vm", Args()) }

// Proxy renders vm/proxy.yaml with ProxyArgs.
func Proxy(t *testing.T) string { return mustRender(t, "vm/proxy.yaml", "vm", ProxyArgs()) }

// Asset returns an embedded file verbatim - the provision scripts and
// vm/verify.sh, which some assertions read unrendered.
func Asset(t *testing.T, name string) string {
	t.Helper()
	body, err := ptrbox.Assets.ReadFile(name)
	if err != nil {
		t.Fatalf("reading embedded %s: %v", name, err)
	}
	return string(body)
}

func mustRender(t *testing.T, template, includeDir string, values render.Values) string {
	t.Helper()
	var buf bytes.Buffer
	if err := render.Render(&buf, ptrbox.Assets, template, includeDir, values); err != nil {
		t.Fatalf("rendering %s: %v", template, err)
	}
	return buf.String()
}
