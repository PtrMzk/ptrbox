package invariants

// The cloud-init renderings: the same two VMs as user-data for the Multipass
// backend (vm/claude-repo.cloud-init.yaml, vm/proxy.cloud-init.yaml).
//
// Every assertion in invariants_test.go about what the provision scripts DO
// holds here by construction, and the construction is what this file pins:
// the cloud-init sandbox embeds each vm/provision/*.sh line for line, as the
// same renderer renders it with this backend's values, and nothing else runs.
// So the rules that are about the scripts are asserted once, against lima's
// rendering, and this file asserts that the second rendering carries the same
// scripts - plus everything that has no lima counterpart: the two accounts,
// the marker verify.sh reads, the second NIC, and the proxy's address in
// place of lima's gateway.

import (
	"path"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/PtrMzk/ptrbox/internal/rendertest"
)

func cloudInit(t *testing.T) (rendered, stripped string) {
	t.Helper()
	rendered = rendertest.CloudInit(t)
	return rendered, stripComments(rendered)
}

func proxyCloudInit(t *testing.T) (rendered, stripped string) {
	t.Helper()
	rendered = rendertest.ProxyCloudInit(t)
	return rendered, stripComments(rendered)
}

// written is one write_files entry as the rendering spells it.
type written struct {
	path, permissions string
	content           []string
}

// writeFiles parses a rendering's write_files block. Line-based, on the
// rendered text rather than the comment-stripped one (a literal block's
// shebang is a comment to stripComments), and exactly as strict as the
// templates are regular: an entry is `  - path:`, its keys sit at four
// spaces, and its content is the six-space block after `content: |`, up to
// the next entry or top-level key. Anything else in the block fails the test
// rather than being skipped - a line this parser does not understand is a
// line no assertion below is looking at.
func writeFiles(t *testing.T, rendered string) []written {
	t.Helper()
	var files []written
	in, content := false, false
	for _, line := range strings.Split(rendered, "\n") {
		if line == "write_files:" {
			in = true
			continue
		}
		if in && line != "" && isTopLevelKey(line[0]) {
			in = false
		}
		if !in {
			continue
		}
		switch {
		case strings.HasPrefix(line, "  - path: "):
			files = append(files, written{path: strings.TrimPrefix(line, "  - path: ")})
			content = false
		case len(files) > 0 && strings.HasPrefix(line, "    permissions: "):
			files[len(files)-1].permissions = strings.TrimPrefix(line, "    permissions: ")
		case len(files) > 0 && line == "    content: |":
			content = true
		case content && (line == "" || strings.HasPrefix(line, "      ")):
			files[len(files)-1].content = append(files[len(files)-1].content, strings.TrimPrefix(line, "      "))
		case line == "" || strings.HasPrefix(strings.TrimSpace(line), "#"):
			// Blank, or a comment between entries.
		default:
			t.Fatalf("write_files: a line the invariants do not understand: %q", line)
		}
	}
	if len(files) == 0 {
		t.Fatal("no write_files entries")
	}
	for i := range files {
		for n := len(files[i].content); n > 0 && files[i].content[n-1] == ""; n-- {
			files[i].content = files[i].content[:n-1]
		}
	}
	return files
}

func fileAt(t *testing.T, files []written, p string) written {
	t.Helper()
	for _, f := range files {
		if f.path == p {
			return f
		}
	}
	t.Fatalf("write_files has no entry for %s", p)
	return written{}
}

// lines is a rendered script as the write_files block should carry it.
func lines(script string) []string {
	out := strings.Split(script, "\n")
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}

func topLevelKeys(rendered string) []string {
	var keys []string
	for _, line := range strings.Split(rendered, "\n") {
		if line != "" && isTopLevelKey(line[0]) {
			keys = append(keys, strings.TrimSuffix(line, ":"))
		}
	}
	return keys
}

// block is the lines of one top-level key, comments stripped, blanks dropped.
func block(stripped, key string) []string {
	var out []string
	in := false
	for _, line := range strings.Split(stripped, "\n") {
		if line == key+":" {
			in = true
			continue
		}
		if in && line != "" && isTopLevelKey(line[0]) {
			in = false
		}
		if in && strings.TrimSpace(line) != "" {
			out = append(out, strings.TrimRight(line, " "))
		}
	}
	return out
}

func both(t *testing.T) map[string]string {
	t.Helper()
	return map[string]string{"sandbox": rendertest.CloudInit(t), "proxy": rendertest.ProxyCloudInit(t)}
}

// --- the document ------------------------------------------------------------

func TestCloudInitRenderingsAreCloudConfig(t *testing.T) {
	// The first line is what cloud-init keys on; a header comment above it
	// would make the whole document a shell script to it.
	for name, rendered := range both(t) {
		if !strings.HasPrefix(rendered, "#cloud-config\n") {
			t.Errorf("the %s rendering does not begin with #cloud-config", name)
		}
	}
}

// Two keys and no others. Everything a guest does is a provision script under
// vm/, where it is reviewed, linted and in part executed by the suite; a
// `runcmd:`, `packages:` or `bootcmd:` here would be a second place that
// decides what a sandbox contains, and `mounts:` or `ssh_authorized_keys:`
// would be the two things this design keeps out of a guest, spelled in a
// dialect no other invariant reads.
func TestCloudInitSaysOnlyWhoTheUsersAreAndWhatFilesExist(t *testing.T) {
	for name, rendered := range both(t) {
		if keys := topLevelKeys(rendered); !slices.Equal(keys, []string{"users", "write_files"}) {
			t.Errorf("the %s rendering has top-level keys %v, want exactly [users write_files]", name, keys)
		}
	}
}

// --- the accounts ------------------------------------------------------------

// The two-user model, spelled exactly. The default user is the daemon's and
// keeps what cloud-init gives it; the agent is the second entry with a pinned
// uid, a locked password, a shell, and nothing else - no `sudo`, no `groups`,
// no keys. Exact equality rather than a list of forbidden keys, so that a new
// key is a failed test and the diff to this file is its argument.
func TestTheSandboxHasTheDaemonUserAndAnAgentWithNothingGranted(t *testing.T) {
	_, stripped := cloudInit(t)
	want := []string{
		"  - default",
		"  - name: agent",
		"    uid: 1000",
		"    lock_passwd: true",
		"    shell: /bin/bash",
	}
	if got := block(stripped, "users"); !slices.Equal(got, want) {
		t.Errorf("users block is\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestTheProxyHasOnlyTheDefaultUser(t *testing.T) {
	_, stripped := proxyCloudInit(t)
	if got := block(stripped, "users"); !slices.Equal(got, []string{"  - default"}) {
		t.Errorf("proxy users block is %q, want exactly the default user", got)
	}
	mustNotMatch(t, stripped, `\bagent\b|runuser`, "the proxy VM has an agent account or runs something as one")
}

// --- the marker --------------------------------------------------------------

// verify.sh reads /var/lib/ptrbox/backend for what differs by backend, and
// trusts it because root wrote it. So: root-owned with the default mode (no
// `permissions` key, which is also the key Multipass's re-emission mangles),
// keys spelled as verify.sh's sed lines read them, and the daemon user the
// same literal 90-harden.sh is rendered with in the same document.
func TestTheBackendMarkerIsSpelledAsVerifyReadsIt(t *testing.T) {
	rendered, _ := cloudInit(t)
	marker := fileAt(t, writeFiles(t, rendered), "/var/lib/ptrbox/backend")
	if marker.permissions != "" {
		t.Errorf("the marker sets permissions %q; the default (root, 0644) is what verify.sh trusts", marker.permissions)
	}
	want := []string{"backend=multipass", "mount-type=fuse.sshfs", "daemon-user=ubuntu"}
	if !slices.Equal(marker.content, want) {
		t.Errorf("marker content is %q, want %q", marker.content, want)
	}
	mustMatch(t, rendered, `(?m)^\s*DAEMON_USER="ubuntu"$`,
		"90-harden.sh is not rendered with the marker's daemon user")

	verify := asset(t, "vm/verify.sh")
	for _, want := range []string{`state="${1:-/var/lib/ptrbox}"`, `marker="$state/backend"`,
		`'s/^mount-type=//p'`, `'s/^daemon-user=//p'`} {
		if !strings.Contains(verify, want) {
			t.Errorf("vm/verify.sh does not read the marker the template writes: missing %s", want)
		}
	}

	proxy := fileAt(t, writeFiles(t, rendertest.ProxyCloudInit(t)), "/var/lib/ptrbox/backend")
	if !slices.Equal(proxy.content, []string{"backend=multipass"}) || proxy.permissions != "" {
		t.Errorf("proxy marker is %q (permissions %q), want backend=multipass alone, default mode", proxy.content, proxy.permissions)
	}
}

// --- the scripts -------------------------------------------------------------

// Both templates include every provision script exactly once, by marker.
func TestBothSandboxTemplatesIncludeEveryProvisionScriptOnce(t *testing.T) {
	for _, template := range []string{"vm/claude-repo.yaml", "vm/claude-repo.cloud-init.yaml"} {
		body := asset(t, template)
		for _, name := range provisionScripts(t, "vm/provision") {
			marker := "__INCLUDE:provision/" + path.Base(name) + "__"
			if n := strings.Count(body, marker); n != 1 {
				t.Errorf("%s includes %s %d times, want once", template, path.Base(name), n)
			}
		}
	}
}

// The cloud-init sandbox runs the same scripts lima does, installed as
// cloud-init's per-boot scripts: root, every boot, lexical order, shebang on
// line one because cloud-init execs the file. Each root-side script is
// embedded line for line as the renderer renders it with this backend's
// values; each user-side one (30, 40) is a root-owned body the agent can read
// and not write, run through a wrapper as the agent and as nobody else.
func TestEveryProvisionScriptRunsPerBootAsCloudInitInstallsIt(t *testing.T) {
	rendered, stripped := cloudInit(t)
	perBoot, bodies := map[string]written{}, map[string]written{}
	var order []string
	for _, f := range writeFiles(t, rendered) {
		dir, base := path.Split(f.path)
		switch dir {
		case "/var/lib/cloud/scripts/per-boot/":
			perBoot[base] = f
			order = append(order, base)
		case "/usr/local/lib/ptrbox/":
			bodies[base] = f
		}
	}
	if !sort.StringsAreSorted(order) {
		t.Errorf("per-boot scripts are not written in lexical order: %v", order)
	}

	var want, got []string
	for _, name := range provisionScripts(t, "vm/provision") {
		want = append(want, path.Base(name))
	}
	for base := range perBoot {
		got = append(got, base)
	}
	sort.Strings(got)
	if !slices.Equal(got, want) {
		t.Fatalf("per-boot scripts are %v, want exactly vm/provision/*.sh: %v", got, want)
	}

	userSide := map[string]bool{"30-toolchain.sh": true, "40-userenv.sh": true}
	for _, name := range provisionScripts(t, "vm/provision") {
		base := path.Base(name)
		f := perBoot[base]
		if f.permissions != "0o755" {
			t.Errorf("%s has permissions %q, want 0o755 (a string to cloud-init's parser after Multipass unquotes it)", f.path, f.permissions)
		}
		if len(f.content) == 0 || f.content[0] != "#!/bin/bash" {
			t.Errorf("%s does not begin with the bash shebang; cloud-init execs it", f.path)
		}
		expect := lines(rendertest.Script(t, name, rendertest.CloudInitArgs()))
		if !userSide[base] {
			if !slices.Equal(f.content, expect) {
				t.Errorf("%s is not %s rendered line for line", f.path, name)
			}
			if _, dup := bodies[base]; dup {
				t.Errorf("%s is a root-side script and also has a body under /usr/local/lib/ptrbox", base)
			}
			continue
		}
		wrapper := []string{"#!/bin/bash", "exec runuser -l agent -c 'bash /usr/local/lib/ptrbox/" + base + "'"}
		if !slices.Equal(f.content, wrapper) {
			t.Errorf("%s is\n%s\nwant exactly the agent wrapper\n%s", f.path, strings.Join(f.content, "\n"), strings.Join(wrapper, "\n"))
		}
		body, ok := bodies[base]
		if !ok {
			t.Errorf("no body for %s under /usr/local/lib/ptrbox", base)
			continue
		}
		if body.permissions != "" {
			t.Errorf("%s sets permissions %q; the default is root-owned and agent-readable, which is the point", body.path, body.permissions)
		}
		if !slices.Equal(body.content, expect) {
			t.Errorf("%s is not %s rendered line for line", body.path, name)
		}
	}
	if len(bodies) != 2 {
		t.Errorf("/usr/local/lib/ptrbox holds %d scripts, want the two user-side ones", len(bodies))
	}

	// The only account anything switches to is the agent. A `runuser -l
	// ubuntu`, a `su -`, a `sudo -u` anywhere would be the daemon user's root
	// reached from a script the agent's environment shapes.
	for _, m := range regexp.MustCompile(`runuser[^\n]*|\bsu -[^\n]*|sudo -u [^\n]*`).FindAllString(stripped, -1) {
		if !strings.HasPrefix(m, "runuser -l agent -c ") {
			t.Errorf("something switches account other than to the agent: %q", m)
		}
	}
}

func TestTheProxyRunsSquidsScriptPerBootAndNothingElse(t *testing.T) {
	rendered, stripped := proxyCloudInit(t)
	var perBoot []written
	for _, f := range writeFiles(t, rendered) {
		if strings.HasPrefix(f.path, "/var/lib/cloud/scripts/per-boot/") {
			perBoot = append(perBoot, f)
		}
	}
	if len(perBoot) != 1 || path.Base(perBoot[0].path) != "10-squid.sh" {
		t.Fatalf("the proxy's per-boot scripts are %v, want 10-squid.sh alone", perBoot)
	}
	if perBoot[0].permissions != "0o755" {
		t.Errorf("10-squid.sh has permissions %q, want 0o755", perBoot[0].permissions)
	}
	if expect := lines(rendertest.Script(t, "vm/provision-proxy/10-squid.sh", rendertest.ProxyArgs())); !slices.Equal(perBoot[0].content, expect) {
		t.Error("10-squid.sh is not vm/provision-proxy/10-squid.sh rendered line for line")
	}
	mustMatch(t, rendered, `apt-get install -y squid`, "the proxy VM does not install squid")
	mustNotMatch(t, stripped, `nvm|node|claude|playwright|build-essential|/workspace`,
		"the proxy VM carries sandbox toolchain or names the repo mount")
}

// --- the switch --------------------------------------------------------------

// The second NIC gets one static address on the ptrbox switch and nothing
// that would make it a route out: no gateway, no resolver, no DHCP. The
// address is a sandbox slot's (.16 up), never the host's .1 or the proxy's
// .2, and it is eth1 - Multipass's own netplan claims the first NIC by MAC.
func TestTheSandboxGetsOneStaticAddressOnTheSwitchAndNoRoute(t *testing.T) {
	rendered, _ := cloudInit(t)
	netplan := fileAt(t, writeFiles(t, rendered), "/etc/netplan/60-ptrbox.yaml")
	if netplan.permissions != "0o600" {
		t.Errorf("netplan file has permissions %q, want 0o600", netplan.permissions)
	}
	body := strings.Join(netplan.content, "\n")
	mustMatch(t, body, `(?m)^    eth1:$`, "the static address is not on eth1")
	mustNotMatch(t, body, `eth0|gateway4|routes:|nameservers:|dhcp4|dhcp6`,
		"the switch NIC is configured as more than an address")
	addrs := regexp.MustCompile(`addresses: \[([^\]]*)\]`).FindAllStringSubmatch(body, -1)
	if len(addrs) != 1 {
		t.Fatalf("netplan declares %d address lists, want 1", len(addrs))
	}
	m := regexp.MustCompile(`^172\.31\.255\.(\d+)/24$`).FindStringSubmatch(addrs[0][1])
	if m == nil {
		t.Fatalf("address is %q, want one 172.31.255.x/24", addrs[0][1])
	}
	if n, _ := strconv.Atoi(m[1]); n < 16 || n > 254 {
		t.Errorf("address 172.31.255.%d is not a sandbox slot (.16 and up; .1 is the host, .2 the proxy)", n)
	}
}

func TestTheProxyIsAtTheAddressEverySandboxNamesAsItsProxy(t *testing.T) {
	rendered, _ := proxyCloudInit(t)
	netplan := fileAt(t, writeFiles(t, rendered), "/etc/netplan/60-ptrbox.yaml")
	if netplan.permissions != "0o600" {
		t.Errorf("netplan file has permissions %q, want 0o600", netplan.permissions)
	}
	body := strings.Join(netplan.content, "\n")
	mustMatch(t, body, `(?m)^    eth1:$`, "the proxy's static address is not on eth1")
	mustMatch(t, body, `addresses: \[172\.31\.255\.2/24\]`, "the proxy is not at 172.31.255.2")
	if n := countMatches(body, `addresses:`); n != 1 {
		t.Errorf("the proxy declares %d address lists, want 1", n)
	}
	mustNotMatch(t, body, `eth0|gateway4|routes:|nameservers:|dhcp4|dhcp6`,
		"the proxy's switch NIC is configured as more than an address")
	// And that literal is what the sandbox rendering dials.
	_, sandbox := cloudInit(t)
	mustMatch(t, sandbox, `ip daddr 172\.31\.255\.2 tcp dport 8889 accept`,
		"the sandbox does not dial the proxy at the address the proxy template gives it")
}

// --- default-deny egress, at the switch address --------------------------------

// The wall is the same five rules with the proxy at its switch address in
// place of lima's gateway, and lima's gateway appears nowhere: a 192.168.5.2
// in this rendering would be a rule toward an address that exists only on a
// Mac.
func TestTheCloudInitSandboxDialsTheProxyAtTheSwitchAddressOnly(t *testing.T) {
	rendered, stripped := cloudInit(t)
	if n := len(acceptRules(stripped)); n != 5 {
		t.Errorf("the firewall has %d accept rules, want exactly 5", n)
	}
	mustMatch(t, stripped, `policy drop;`, "the firewall chain does not default to drop")
	if n := countMatches(stripped, `ip daddr 172\.31\.255\.2 tcp dport \d+ accept`); n != 1 {
		t.Errorf("%d rules toward the proxy, want exactly one (its own port)", n)
	}
	mustNotMatch(t, stripped, `192\.168\.5\.2`, "lima's gateway leaked into the Multipass rendering")
	mustNotMatch(t, stripped, `dport 1234 accept`, "an LM Studio rule is rendered; the backend has no host services")
	mustMatch(t, rendered, `export HTTPS_PROXY="http://172\.31\.255\.2:8889"`,
		"the guest does not point at the proxy's switch address")
	if n := countMatches(rendered, `HTTPS_PROXY=`); n != 1 {
		t.Errorf("HTTPS_PROXY is set %d times, want 1", n)
	}
	mustMatch(t, rendered, `export NO_PROXY="localhost,127\.0\.0\.1,172\.31\.255\.2"`,
		"the proxy's own address is not exempt from the proxy environment")
}

// --- no credentials ----------------------------------------------------------

func TestNoCredentialsAreBakedIntoTheCloudInitConfigs(t *testing.T) {
	// `lock_passwd` is not `password`; the regex is the lima test's.
	for name, rendered := range both(t) {
		mustNotMatch(t, stripComments(rendered), `(?i)oauth|token|password|api[_-]?key|secret`,
			"the "+name+" cloud-init rendering mentions a credential")
	}
	mustNotMatch(t, stripComments(rendertest.CloudInitWith(t, rendertest.OpencodeOn())),
		`(?i)oauth|token|password|api[_-]?key|secret`,
		"the opencode cloud-init rendering mentions a credential")
}
