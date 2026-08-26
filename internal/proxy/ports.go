package proxy

// Per-sandbox proxy ports.
//
// Squid cannot tell sandboxes apart by source - the usernet relay and the
// loopback forward deliver every VM as 127.0.0.1 - so the port a sandbox
// dials is its identity at the proxy. The identity is enforced, not claimed:
// the port is rendered into the guest's root-owned nftables ruleset at create
// time, and the agent has no sudo to widen it, so VM A physically cannot dial
// VM B's port.
//
// An allocation is a `<vm>.proxy-port` sidecar next to the VM's rendered
// config in the generated dir: the directory that already answers "which VMs
// are ptrbox's" for StopIfIdle. `rm` deletes the sidecar with the config; a
// re-run of a `new` that failed partway reuses the port it already holds, so
// a crashed create cannot leak a slot that retrying would need.

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/PtrMzk/ptrbox/internal/config"
)

const portFileSuffix = ".proxy-port"

// PortFile is the sidecar recording one VM's proxy port.
func PortFile(name string) string {
	return filepath.Join(config.GeneratedDir(), name+portFileSuffix)
}

// PortAllocations reads every sidecar in the generated dir: VM name -> port.
// A missing directory is certainty about zero allocations, not an error.
func PortAllocations() (map[string]int, error) {
	entries, err := os.ReadDir(config.GeneratedDir())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return map[string]int{}, nil
		}
		return nil, err
	}
	ports := map[string]int{}
	for _, entry := range entries {
		name, isPort := strings.CutSuffix(entry.Name(), portFileSuffix)
		if !isPort {
			continue
		}
		body, err := os.ReadFile(filepath.Join(config.GeneratedDir(), entry.Name()))
		if err != nil {
			return nil, err
		}
		port, err := strconv.Atoi(strings.TrimSpace(string(body)))
		if err != nil {
			return nil, fmt.Errorf("%s does not hold a port number: %q",
				PortFile(name), strings.TrimSpace(string(body)))
		}
		ports[name] = port
	}
	return ports, nil
}

// AllocatePort gives a VM the lowest free port in the sandbox range, or the
// one it already holds. The sidecar is written before the caller renders
// anything, so the rendered firewall and the recorded allocation cannot
// disagree.
func AllocatePort(cfg *config.Config, name string) (int, error) {
	held, err := PortAllocations()
	if err != nil {
		return 0, err
	}
	if port, ok := held[name]; ok {
		return port, nil
	}

	inUse := map[int]bool{}
	for _, port := range held {
		inUse[port] = true
	}
	for port := cfg.SandboxPortMin(); port <= cfg.SandboxPortMax(); port++ {
		if inUse[port] {
			continue
		}
		if err := os.MkdirAll(config.GeneratedDir(), 0o755); err != nil {
			return 0, err
		}
		if err := os.WriteFile(PortFile(name), []byte(fmt.Sprintf("%d\n", port)), 0o644); err != nil {
			return 0, err
		}
		return port, nil
	}
	return 0, fmt.Errorf("all %d sandbox proxy ports are allocated - remove a VM you are done with (ptrbox rm) before creating another",
		config.SandboxProxyPorts)
}

// ReleasePort frees a VM's port. Absent is fine: releasing twice, or releasing
// a VM that predates per-VM ports, must not fail an rm.
func ReleasePort(name string) error {
	if err := os.Remove(PortFile(name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// sandboxHTTPPorts is the http_port block rendered into squid.conf: one
// listener per sandbox slot, pinned open statically so that VM churn never
// changes the listener set (and with it the lima forwards, and with them the
// need to restart anything).
func sandboxHTTPPorts(cfg *config.Config) string {
	var lines []string
	for port := cfg.SandboxPortMin(); port <= cfg.SandboxPortMax(); port++ {
		lines = append(lines, fmt.Sprintf("http_port %d", port))
	}
	return strings.Join(lines, "\n")
}
