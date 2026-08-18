package cli

// Shared setup for the command tests.
//
// Each test gets a throwaway HOME, a fake limactl (which also simulates the
// proxy VM's filesystem) and a fake Keychain. Every h.run is a fresh Env, the
// way a fresh process would be, so state has to survive in HOME and in the
// fake rather than in a leftover struct - which is what makes these tests
// stand in for "did install/provisioning/teardown actually work".
//
// git is real, because its behaviour is part of what is being asserted.

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	ptrbox "github.com/PtrMzk/ptrbox"
	"github.com/PtrMzk/ptrbox/internal/config"
	"github.com/PtrMzk/ptrbox/internal/lima"
	"github.com/PtrMzk/ptrbox/internal/lima/limafake"
	"github.com/PtrMzk/ptrbox/internal/narrate"
	"github.com/PtrMzk/ptrbox/internal/proxy"
	"github.com/PtrMzk/ptrbox/internal/ui"
)

type fakeKeychain struct {
	available bool
	token     string
}

func (k *fakeKeychain) Available() bool     { return k.available }
func (k *fakeKeychain) Token(string) string { return k.token }

type harness struct {
	t        *testing.T
	fake     *limafake.Fake
	keychain *fakeKeychain

	home    string
	tmp     string
	repos   string
	exe     string
	editor  func(path string) error
	tty     bool
	missing map[string]bool // tools this host pretends not to have

	// portInUse answers for the host's TCP ports; see newHarness.
	portInUse func(port int) bool

	// verbose is --verbose; narrator is the limactl output translator, built
	// per run and kept so a test can replay it.
	verbose  bool
	narrator *narrate.Stream

	// Result of the most recent run.
	stderr string
	stdout string
	err    error
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	// Physically resolved, because `new` resolves the repo path it is given
	// and the assertions compare against it. On macOS t.TempDir() lands under
	// /var/folders, and /var is a symlink to /private/var - so an unresolved
	// tmp here means every path comparison fails on a Mac and passes on
	// Linux, which is the worst of both.
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(tmp, "home")
	mkdir(t, home)

	// A stray PTRBOX_* in the developer's environment outranks the config
	// file, so clear the lot.
	for _, key := range config.Keys {
		if _, ok := os.LookupEnv("PTRBOX_" + key); ok {
			t.Setenv("PTRBOX_"+key, "")
		}
		os.Unsetenv("PTRBOX_" + key)
	}
	t.Setenv("HOME", home)
	t.Setenv("PTRBOX_CONFIG", filepath.Join(tmp, "ptrbox.conf"))
	if err := os.WriteFile(filepath.Join(tmp, "ptrbox.conf"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PTRBOX_REPO_ROOT", filepath.Join(tmp, "code"))
	// Deterministic git identity: without this, `new` picks up whatever the
	// machine running the tests has configured.
	t.Setenv("PTRBOX_GIT_USER_NAME", "Test Dev")
	t.Setenv("PTRBOX_GIT_USER_EMAIL", "test@example.com")
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")

	// A stand-in for the installed binary, so the PATH symlink offer has
	// something real to point at.
	exe := filepath.Join(tmp, "bin-src", "ptrbox")
	mkdir(t, filepath.Dir(exe))
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	h := &harness{
		t:        t,
		fake:     limafake.New(),
		keychain: &fakeKeychain{available: true},
		home:     home,
		tmp:      tmp,
		repos:    filepath.Join(tmp, "code"),
		exe:      exe,
		editor:   func(string) error { return nil },
		missing:  map[string]bool{},
	}

	// Tools are answered from the harness, so a test can describe a host
	// without limactl without touching the real PATH.
	realLookPath := lookPath
	lookPath = func(tool string) bool { return !h.missing[tool] && realLookPath(tool) }

	// The proxy's port forward exists exactly while the proxy VM is running,
	// so the fake's VM state is what answers "is the forward up". A test that
	// wants a different host - a foreign listener, or Lima failing to publish
	// the forward - replaces h.portInUse.
	realPortInUse := portInUse
	h.portInUse = func(int) bool {
		return h.fake.VMStatus(config.ProxyVM) == lima.StatusRunning
	}
	portInUse = func(port int) bool { return h.portInUse(port) }
	t.Cleanup(func() { lookPath, portInUse = realLookPath, realPortInUse })

	return h
}

// run executes one ptrbox invocation against a freshly built Env.
func (h *harness) run(args ...string) error {
	h.t.Helper()
	stderr, stdout := &bytes.Buffer{}, &bytes.Buffer{}

	// Wired the way main wires it: limactl's output goes through the
	// translator, on stderr. Kept here rather than pointed straight at the
	// buffers so that what these tests read is what a user reads.
	h.narrator = &narrate.Stream{Out: ui.Printer{W: stderr}, Verbose: h.verbose}

	env := &Env{
		Assets:      ptrbox.Assets,
		Out:         ui.Printer{W: stderr},
		Stdout:      stdout,
		Stdin:       strings.NewReader(""),
		Keychain:    h.keychain,
		Exe:         h.exe,
		Interactive: h.tty,
		Editor:      func(path string) error { return h.editor(path) },
		// A fixed clock, so an archive filename is the same on every run.
		Now:  func() time.Time { return time.Date(2026, 8, 17, 20, 45, 0, 0, time.UTC) },
		Lima: &lima.Client{Runner: h.fake, Stdout: h.narrator, Stderr: h.narrator},
	}
	if h.missing["limactl"] {
		env.Lima = &lima.Client{Runner: unavailableRunner{h.fake}, Stdout: h.narrator, Stderr: h.narrator}
	}
	env.Load = func(e *Env) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		// main names the image for the narrator here too - it is the first
		// moment anyone knows the distro.
		h.narrator.Image = cfg.Distro
		e.Cfg = cfg
		e.Proxy = &proxy.Proxy{Cfg: cfg, Lima: e.Lima, Assets: e.Assets, Out: e.Out}
		return nil
	}

	h.err = Run(env, args)
	h.stderr, h.stdout = stderr.String(), stdout.String()
	return h.err
}

// unavailableRunner is a fake limactl that is not installed.
type unavailableRunner struct{ *limafake.Fake }

func (unavailableRunner) Available() bool { return false }

// mustRun fails the test if the command did.
func (h *harness) mustRun(args ...string) {
	h.t.Helper()
	if err := h.run(args...); err != nil {
		h.t.Fatalf("ptrbox %s: %v\nstderr:\n%s", strings.Join(args, " "), err, h.stderr)
	}
}

// output is everything the run produced: both streams plus the returned
// error, since main is what turns an error into a printed line.
func (h *harness) output() string {
	out := h.stdout + h.stderr
	if h.err != nil {
		out += "ptrbox: error: " + h.err.Error() + "\n"
	}
	return out
}

func (h *harness) assertOutputContains(want string) {
	h.t.Helper()
	if !strings.Contains(h.output(), want) {
		h.t.Errorf("output does not mention %q:\n%s", want, h.output())
	}
}

func (h *harness) assertCalled(pattern string) {
	h.t.Helper()
	if !h.fake.Called(pattern) {
		h.t.Errorf("expected a call matching %q; got:\n%s", pattern, h.fake.CallLog())
	}
}

func (h *harness) assertNotCalled(pattern string) {
	h.t.Helper()
	if h.fake.Called(pattern) {
		h.t.Errorf("unexpected call matching %q; got:\n%s", pattern, h.fake.CallLog())
	}
}

func (h *harness) assertOrder(first, second string) {
	h.t.Helper()
	if !h.fake.InOrder(first, second) {
		h.t.Errorf("expected %q before %q; got:\n%s", first, second, h.fake.CallLog())
	}
}

// proxyFile reads a path from the proxy VM's simulated filesystem.
func (h *harness) proxyFile(path string) string {
	h.t.Helper()
	body, ok := h.fake.ReadFile(config.ProxyVM, path)
	if !ok {
		h.t.Fatalf("the proxy VM has no %s", path)
	}
	return body
}

func (h *harness) allowlist() string {
	h.t.Helper()
	body, err := os.ReadFile(config.AllowlistPath())
	if err != nil {
		h.t.Fatal(err)
	}
	return string(body)
}

// generated reads a rendered Lima config.
func (h *harness) generated(name string) string {
	h.t.Helper()
	body, err := os.ReadFile(config.GeneratedConfig(name))
	if err != nil {
		h.t.Fatal(err)
	}
	return string(body)
}

func (h *harness) exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func mkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

// containsLine reports whether body has want as a whole line - the equivalent
// of `grep -qx`, which several assertions need so that an example domain in a
// comment does not count as a match.
func containsLine(body, want string) bool {
	for _, line := range strings.Split(body, "\n") {
		if line == want {
			return true
		}
	}
	return false
}

// matches is a regexp convenience for assertions about rendered configs.
func matches(body, pattern string) bool {
	return regexp.MustCompile(pattern).MatchString(body)
}

// --- real git ----------------------------------------------------------------
// git is not faked: `new` runs it for real, and what it does to the host clone
// (the hooks path, above all) is part of what these tests assert.

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func gitConfig(t *testing.T, dir, key string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "config", key).Output()
	if err != nil {
		t.Fatalf("git config %s: %v", key, err)
	}
	return strings.TrimSpace(string(out))
}

// printer is a discard-free Printer for helpers tested outside a full run.
func (h *harness) printer() ui.Printer { return ui.Printer{W: &bytes.Buffer{}} }

// manifestLinks counts the symlinks install recorded. Asserting on the
// manifest rather than on the printed prose keeps the check independent of
// what the temp directory happens to be called - a path containing the word
// "linked" fooled the substring version of this.
func (h *harness) manifestLinks() int {
	h.t.Helper()
	body, err := os.ReadFile(filepath.Join(config.Dir(), "install-manifest"))
	if err != nil {
		return 0
	}
	n := 0
	for _, line := range strings.Split(string(body), "\n") {
		if strings.HasPrefix(line, "linked ") {
			n++
		}
	}
	return n
}
