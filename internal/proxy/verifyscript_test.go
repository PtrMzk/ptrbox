package proxy_test

// vm/verify-proxy.sh, run for real.
//
// Everywhere else in the suite the proxy VM is a fake, which proves that
// ptrbox pipes the script in and fails the install when it fails - but not
// that the script itself asks the right questions. It is the gate on install's
// success message, so the questions are the part that matters.
//
// So: a stub squid on a real socket, a real bash, and the script's own
// defaults replaced by temp files. The stub answers the way squid does - 200
// to a CONNECT for the domain it likes, 403 to anything else - which is enough
// to exercise the parts that can silently rot: the /dev/tcp handshake, the
// status-line parsing, and the choice of probe domain from the allowlist.
//
// systemd is stubbed rather than skipped: the script must stay free to assert
// the service state, and a test host is not a proxy VM.

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	ptrbox "github.com/PtrMzk/ptrbox"
)

// stubSquid answers CONNECT requests: 200 for allow, 403 for everything else.
// It records the hostnames it was asked for, which is how the test sees which
// domain the script picked out of the allowlist.
type stubSquid struct {
	listener net.Listener
	asked    chan string
}

func newStubSquid(t *testing.T, allow string) *stubSquid {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &stubSquid{listener: listener, asked: make(chan string, 8)}
	t.Cleanup(func() { listener.Close() })

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				host, ok := readRequest(conn)
				if !ok {
					return
				}
				s.asked <- host
				if host == allow {
					fmt.Fprint(conn, "HTTP/1.1 200 Connection established\r\n\r\n")
					return
				}
				fmt.Fprint(conn, "HTTP/1.1 403 Forbidden\r\n\r\n")
			}()
		}
	}()
	return s
}

// readRequest reads the WHOLE request - the CONNECT line and the headers after
// it, up to the blank line - and returns the host it names.
//
// Reading all of it is not politeness, it is the difference between a test
// that passes and one that passes most of the time. Closing a socket with
// unread bytes still in its receive buffer makes Linux send an RST rather than
// a FIN, and the script writes its request in more than one syscall: answer
// after the first line and the RST can land between the script's writes, where
// it arrives as SIGPIPE and kills the subshell mid-request. The script then
// reports "answered nothing" for a proxy that answered - which is the shape of
// every real squid failure this file exists to catch, arriving at random on a
// loaded machine. Real squid reads the request before it replies; so does this.
func readRequest(conn net.Conn) (host string, ok bool) {
	reader := bufio.NewReader(conn)
	for i := 0; ; i++ {
		line, err := reader.ReadString('\n')
		if err != nil {
			return host, host != ""
		}
		if i == 0 {
			fields := strings.Fields(line)
			if len(fields) < 2 {
				return "", false
			}
			host = strings.TrimSuffix(fields[1], ":443")
		}
		if strings.TrimRight(line, "\r\n") == "" {
			return host, host != ""
		}
	}
}

func (s *stubSquid) port() int { return s.listener.Addr().(*net.TCPAddr).Port }

// runVerifyScript runs vm/verify-proxy.sh against the given port and
// allowlist, and returns its combined output plus whether it exited zero.
func runVerifyScript(t *testing.T, port int, allowlist string) (string, bool) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is not available")
	}
	dir := t.TempDir()

	script := filepath.Join(dir, "verify-proxy.sh")
	body, err := ptrbox.Assets.ReadFile("vm/verify-proxy.sh")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script, body, 0o755); err != nil {
		t.Fatal(err)
	}

	conf := filepath.Join(dir, "squid.conf")
	// Shaped like the real one, comment line and all, so the port is found the
	// way it is found in the file ptrbox actually pushes.
	confBody := fmt.Sprintf("# ptrbox-managed v2\n\nhttp_port %d\nacl CONNECT method CONNECT\n", port)
	if err := os.WriteFile(conf, []byte(confBody), 0o644); err != nil {
		t.Fatal(err)
	}

	list := filepath.Join(dir, "allowed_domains.txt")
	if err := os.WriteFile(list, []byte(allowlist), 0o644); err != nil {
		t.Fatal(err)
	}

	// A systemd that says yes. The service check is not what these cases are
	// about, and a stub keeps it from turning every one of them into a
	// failure on a host that is not the proxy VM.
	stubs := filepath.Join(dir, "stubs")
	if err := os.MkdirAll(stubs, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stubs, "systemctl"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("bash", script, conf, list)
	cmd.Env = append(os.Environ(), "PATH="+stubs+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	return string(out), err == nil
}

// The allowlist a real install starts from: comments, blank lines, a
// leading-dot entry, and trailing comments after a domain.
const sampleAllowlist = `# --- Claude Code: required ---

.githubusercontent.com     # a leading dot covers subdomains
api.anthropic.com          # the API itself
pypi.org
`

func TestTheScriptPassesAgainstAWorkingProxy(t *testing.T) {
	squid := newStubSquid(t, "api.anthropic.com")
	out, passed := runVerifyScript(t, squid.port(), sampleAllowlist)
	if !passed {
		t.Fatalf("verify-proxy.sh failed against a working proxy:\n%s", out)
	}
	for _, want := range []string{"squid listening", "allowed domain tunnels", "denied domain refused"} {
		if !strings.Contains(out, want) {
			t.Errorf("no %q check ran:\n%s", want, out)
		}
	}
	if strings.Contains(out, "FAIL") {
		t.Errorf("a check failed against a working proxy:\n%s", out)
	}
}

func TestTheScriptProbesTheFirstFetchableAllowlistEntry(t *testing.T) {
	// Not the first LINE: comments, blanks and leading-dot entries are not
	// destinations, and a probe against one would fail for the wrong reason.
	squid := newStubSquid(t, "api.anthropic.com")
	if _, passed := runVerifyScript(t, squid.port(), sampleAllowlist); !passed {
		t.Fatal("the script did not pass")
	}
	var asked []string
	for len(squid.asked) > 0 {
		asked = append(asked, <-squid.asked)
	}
	if len(asked) == 0 || asked[0] != "api.anthropic.com" {
		t.Errorf("the script probed %v, want api.anthropic.com first", asked)
	}
}

func TestAProxyThatAllowsEverythingFailsTheScript(t *testing.T) {
	// The check that catches an allowlist that never loaded: squid is up,
	// listening, and tunnelling - to anything at all, this domain included.
	openProxy := newStubSquid(t, "blocked.ptrbox.invalid")

	out, passed := runVerifyScript(t, openProxy.port(), sampleAllowlist)
	if passed {
		t.Fatalf("the script passed a proxy that tunnels to a denied domain:\n%s", out)
	}
	if !strings.Contains(out, "answered 200, want 403") {
		t.Errorf("the denial check is not what failed:\n%s", out)
	}
	// The summary is on stderr, and it is what says out loud that the install
	// is over. `exec` with no command applies its redirections permanently, so
	// the 2>/dev/null on the listening probe used to silence every later line
	// of this script - this one included - in exactly the runs where the probe
	// succeeded and something later failed, which is this one. (The silent
	// proxy cannot see it: an exec whose open fails never applies the
	// redirection at all, so that test passed throughout.)
	if !strings.Contains(out, "check(s) FAILED") {
		t.Errorf("the failure summary never reached the caller:\n%s", out)
	}
}

func TestASilentProxyFailsTheScript(t *testing.T) {
	// Squid parsed its config and died on start: this is the exact state that
	// used to report "host setup complete".
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close() // nothing is listening there now

	out, passed := runVerifyScript(t, port, sampleAllowlist)
	if passed {
		t.Fatalf("the script passed with nothing listening:\n%s", out)
	}
	if !strings.Contains(out, "nothing accepts connections on port") {
		t.Errorf("the listening check is not what failed:\n%s", out)
	}
}

func TestAnAllowlistWithNothingFetchableFailsTheScript(t *testing.T) {
	// An allowlist of nothing but comments and subdomain wildcards leaves the
	// script no way to prove egress works, and "could not check" must not read
	// as "checked, fine".
	squid := newStubSquid(t, "api.anthropic.com")
	out, passed := runVerifyScript(t, squid.port(), "# nothing here\n\n.githubusercontent.com\n")
	if passed {
		t.Fatalf("the script passed without proving anything tunnels:\n%s", out)
	}
	if !strings.Contains(out, "no fetchable domain in") {
		t.Errorf("the tunnelling check is not what failed:\n%s", out)
	}
}

func TestAConfigWithNoPortFailsTheScript(t *testing.T) {
	// The host never pushed its config, so squid is serving the distro's
	// stock one - which allows nothing and would look like a network outage.
	dir := t.TempDir()
	conf := filepath.Join(dir, "squid.conf")
	if err := os.WriteFile(conf, []byte("# stock config\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	list := filepath.Join(dir, "allowed_domains.txt")
	if err := os.WriteFile(list, []byte(sampleAllowlist), 0o644); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "verify-proxy.sh")
	body, err := ptrbox.Assets.ReadFile("vm/verify-proxy.sh")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script, body, 0o755); err != nil {
		t.Fatal(err)
	}

	out, err := exec.Command("bash", script, conf, list).CombinedOutput()
	if err == nil {
		t.Fatalf("the script passed with no http_port in the config:\n%s", out)
	}
	if !strings.Contains(string(out), "never pushed its config") {
		t.Errorf("the diagnosis does not name the cause:\n%s", out)
	}
}
