package invariants

// Who squid serves. Every allow line in the squid config is gated on the
// from_forward ACL, and that ACL is rendered from backend.Facts.ProxyClientSrc
// - so the set of addresses a proxy accepts clients from is a per-backend
// fact, and these hold it to the two shapes that exist: lima's loopback
// forward, and a switch network the Multipass sandboxes dial across. Anything
// wider is an open proxy on whatever network the proxy VM happens to have.

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/PtrMzk/ptrbox/internal/lima"
	"github.com/PtrMzk/ptrbox/internal/rendertest"
)

// The Multipass backend's answer, as commit 15 will state it: the sandboxes'
// switch, and the proxy's own loopback for the in-VM verification.
const switchClients = "127.0.0.1 172.31.255.0/24"

// fromForwardSources are the values of every `acl from_forward src` line.
func fromForwardSources(conf string) []string {
	var sources []string
	for _, m := range regexp.MustCompile(`(?m)^acl from_forward src (.+)$`).FindAllStringSubmatch(conf, -1) {
		sources = append(sources, strings.Fields(m[1])...)
	}
	return sources
}

func TestSquidAcceptsClientsOnlyFromTheForwardOrTheSwitch(t *testing.T) {
	permitted := []string{"127.0.0.1", "::1", "172.31.255.0/24"}
	for name, conf := range map[string]string{
		"lima":      rendertest.SquidConf(t),
		"multipass": rendertest.SquidConfFor(t, switchClients),
	} {
		sources := fromForwardSources(conf)
		if len(sources) == 0 {
			t.Fatalf("%s: no from_forward src lines", name)
		}
		for _, src := range sources {
			if !slices.Contains(permitted, src) {
				t.Errorf("%s: squid accepts clients from %q, which is neither the forward nor the switch", name, src)
			}
		}
		if !slices.Contains(sources, "::1") {
			t.Errorf("%s: squid's own loopback is not a client; verify-proxy.sh dials it", name)
		}
		// The gate itself: every allow names the ACL.
		for _, line := range strings.Split(stripComments(conf), "\n") {
			if strings.HasPrefix(line, "http_access allow") && !strings.Contains(line, "from_forward") {
				t.Errorf("%s: an allow rule is not gated on from_forward: %q", name, line)
			}
		}
	}
}

// Lima's bytes did not move: the fact renders to the literal the config
// carried before there was a fact.
func TestLimaSquidServesTheLoopbackForwardAlone(t *testing.T) {
	facts := lima.Backend{}.Facts()
	if !slices.Equal(facts.ProxyClientSrc, []string{"127.0.0.1"}) {
		t.Errorf("lima's ProxyClientSrc is %v, want the loopback forward alone", facts.ProxyClientSrc)
	}
	conf := rendertest.SquidConfFor(t, strings.Join(facts.ProxyClientSrc, " "))
	mustMatch(t, conf, `(?m)^acl from_forward src 127\.0\.0\.1$`, "the lima rule is not the byte-identical loopback one")
	mustMatch(t, conf, `(?m)^acl from_forward src ::1$`, "the v6 loopback line is gone")
	if n := len(fromForwardSources(conf)); n != 2 {
		t.Errorf("lima's squid names %d client sources, want the two loopbacks", n)
	}
}

// The Multipass rendering's switch network is the one the proxy's own
// netplan and the sandboxes' firewall rules name: one /24, spelled the same
// in all three places.
func TestTheSwitchNetworkIsSpelledTheSameAtSquidAndInTheGuests(t *testing.T) {
	if !strings.Contains(switchClients, "172.31.255.0/24") {
		t.Fatal("the fixture's switch network is not 172.31.255.0/24")
	}
	_, sandbox := cloudInit(t)
	mustMatch(t, sandbox, `ip daddr 172\.31\.255\.2 tcp dport`, "the sandbox does not dial the proxy on the switch squid accepts")
	proxy, _ := proxyCloudInit(t)
	mustMatch(t, proxy, `addresses: \[172\.31\.255\.2/24\]`, "the proxy is not on the switch squid accepts")
}
