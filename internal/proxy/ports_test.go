package proxy_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PtrMzk/ptrbox/internal/config"
	"github.com/PtrMzk/ptrbox/internal/proxy"
)

// The allocator hands out each sandbox's identity at the proxy, so what these
// pin is bookkeeping with a security consequence: two VMs sharing a port
// would share an allowlist, and a leaked slot is a sandbox that cannot be
// created.
//
// newHarness is called for its side effects alone here - a throwaway HOME and
// an environment with no PTRBOX_* in it. The allocator reads the port block
// from constants and the sidecars from that HOME, so there is nothing to hold
// on to.

func TestPortsAreAllocatedLowestFirst(t *testing.T) {
	newHarness(t)
	for i, want := range []int{8889, 8890, 8891} {
		name := []string{"one", "two", "three"}[i]
		port, err := proxy.AllocatePort(name)
		if err != nil {
			t.Fatal(err)
		}
		if port != want {
			t.Errorf("%s got port %d, want %d", name, port, want)
		}
	}
}

func TestAnAllocationIsStableAcrossRetries(t *testing.T) {
	// A `new` that failed partway holds its sidecar; the re-run must get the
	// same port back rather than leak the slot and take a second one.
	newHarness(t)
	first, err := proxy.AllocatePort("demo")
	if err != nil {
		t.Fatal(err)
	}
	again, err := proxy.AllocatePort("demo")
	if err != nil {
		t.Fatal(err)
	}
	if first != again {
		t.Errorf("retry got port %d, first run got %d", again, first)
	}
	held, err := proxy.PortAllocations()
	if err != nil {
		t.Fatal(err)
	}
	if len(held) != 1 {
		t.Errorf("one VM holds %d allocations", len(held))
	}
}

func TestAReleasedPortIsReusedByTheNextAllocation(t *testing.T) {
	newHarness(t)
	for _, name := range []string{"one", "two", "three"} {
		if _, err := proxy.AllocatePort(name); err != nil {
			t.Fatal(err)
		}
	}
	if err := proxy.ReleasePort("one"); err != nil {
		t.Fatal(err)
	}
	port, err := proxy.AllocatePort("four")
	if err != nil {
		t.Fatal(err)
	}
	if port != 8889 {
		t.Errorf("the freed slot was not reused: got %d, want 8889", port)
	}
}

func TestReleasingAnUnallocatedPortIsFine(t *testing.T) {
	// rm of a VM that predates per-VM ports, or a double rm, must not fail.
	newHarness(t)
	if err := proxy.ReleasePort("never-existed"); err != nil {
		t.Fatal(err)
	}
}

func TestAFullRangeRefusesTheSeventeenthSandbox(t *testing.T) {
	newHarness(t)
	for i := 0; i < config.SandboxProxyPorts; i++ {
		if _, err := proxy.AllocatePort(string(rune('a'+i))+"-vm"); err != nil {
			t.Fatal(err)
		}
	}
	_, err := proxy.AllocatePort("one-too-many")
	if err == nil || !strings.Contains(err.Error(), "ptrbox rm") {
		t.Errorf("err = %v, want a refusal that says what to do about it", err)
	}
}

func TestRenderedConfigsDoNotCountAsAllocations(t *testing.T) {
	// The generated dir holds the rendered yamls too; only the sidecars are
	// allocations.
	newHarness(t)
	if err := os.MkdirAll(config.GeneratedDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.GeneratedConfig("demo"), []byte("vmType: vz\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	held, err := proxy.PortAllocations()
	if err != nil {
		t.Fatal(err)
	}
	if len(held) != 0 {
		t.Errorf("a rendered config was read as an allocation: %v", held)
	}
	if _, err := proxy.AllocatePort("demo"); err != nil {
		t.Fatal(err)
	}
}

func TestACorruptSidecarIsAnErrorNotAGuess(t *testing.T) {
	// A sidecar that does not hold a number means the state is unreadable;
	// allocating around it could hand out a port some VM's firewall already
	// pins.
	newHarness(t)
	if err := os.MkdirAll(config.GeneratedDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(config.GeneratedDir(), "demo.proxy-port")
	if err := os.WriteFile(bad, []byte("not-a-port\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := proxy.PortAllocations(); err == nil {
		t.Error("a corrupt sidecar was read as an allocation")
	}
}
