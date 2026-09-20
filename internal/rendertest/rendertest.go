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
		"PROXY_HOST":     "192.168.5.2",
		// The VM's own allocated port, not the base port: since item 37 every
		// sandbox dials its own, and 8889 is the first allocation.
		"PROXY_PORT": "8889",
		// Lima: one account, and it loses root. The invariants hold this
		// rendering to an empty daemon user.
		"DAEMON_USER":    "",
		"GIT_USER_NAME":  "Example Dev",
		"GIT_USER_EMAIL": "dev@example.com",
		"CLAUDE_MODEL":   "opus",
		// opencode OFF: the golden shows the default wall, five rules. The
		// on-path is asserted from OpencodeOn below rather than a second
		// golden.
		"OPENCODE":             "false",
		"LMSTUDIO_PORT":        "1234",
		"LMSTUDIO_NFT_RULE":    "# (PTRBOX_OPENCODE off: no LM Studio rule)",
		"OPENCODE_MODELS_JSON": "{}",
		"OPENCODE_MODEL":       "",
	}
}

// OpencodeOn are the overrides that turn the fixture into an opencode sandbox:
// the tool in the list, the flag on, and the one extra firewall rule exactly
// as config.LMStudioNftRule renders it for the default port.
func OpencodeOn() render.Values {
	return render.Values{
		"TOOLCHAIN":            "node opencode uv",
		"OPENCODE":             "true",
		"LMSTUDIO_NFT_RULE":    "ip daddr 192.168.5.2 tcp dport 1234 accept",
		"OPENCODE_MODELS_JSON": `{"qwen/qwen3-coder-30b":{"name":"qwen/qwen3-coder-30b"}}`,
		"OPENCODE_MODEL":       "qwen/qwen3-coder-30b",
	}
}

// SandboxWith renders the sandbox template with Args plus the given overrides.
func SandboxWith(t *testing.T, overrides render.Values) string {
	return mustRender(t, "vm/claude-repo.yaml", "vm", with(Args(), overrides))
}

// CloudInitArgs are the cloud-init sandbox template's values: Args, with the
// Multipass backend's answers in place of lima's - the proxy at its own
// address on the ptrbox switch, a daemon user that keeps root, and the
// sandbox's static address on that switch (the one step 0 used). Everything
// the provision scripts read is otherwise the same, which is what lets an
// invariant compare the two renderings' scripts.
func CloudInitArgs() render.Values {
	return with(Args(), render.Values{
		"PROXY_HOST":  "172.31.255.2",
		"DAEMON_USER": "ubuntu",
		"VM_ADDR":     "172.31.255.17",
	})
}

// CloudInit renders vm/claude-repo.cloud-init.yaml with CloudInitArgs.
func CloudInit(t *testing.T) string {
	return mustRender(t, "vm/claude-repo.cloud-init.yaml", "vm", CloudInitArgs())
}

// CloudInitWith renders the cloud-init sandbox template with CloudInitArgs
// plus the given overrides.
func CloudInitWith(t *testing.T, overrides render.Values) string {
	return mustRender(t, "vm/claude-repo.cloud-init.yaml", "vm", with(CloudInitArgs(), overrides))
}

// ProxyCloudInit renders vm/proxy.cloud-init.yaml. It has no placeholders of
// its own; ProxyArgs is passed so a future one fails the same way.
func ProxyCloudInit(t *testing.T) string {
	return mustRender(t, "vm/proxy.cloud-init.yaml", "vm", ProxyArgs())
}

// Script renders one provision script on its own, with the given values -
// what a rendering that embeds it must contain, line for line.
func Script(t *testing.T, name string, values render.Values) string {
	return mustRender(t, name, "vm", values)
}

func with(values, overrides render.Values) render.Values {
	for k, v := range overrides {
		values[k] = v
	}
	return values
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
