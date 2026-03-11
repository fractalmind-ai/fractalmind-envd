//go:build linux

package wg

import (
	"log"
	"os/exec"
	"strings"
)

// EnableIPForward enables IPv4 forwarding and adds iptables FORWARD rules
// for the WireGuard interface. This is needed on relay nodes so that mesh
// traffic between clients can be routed through this node.
// All operations are idempotent.
func EnableIPForward(ifaceName string) {
	// Enable IP forwarding
	if out, err := exec.Command("sysctl", "-w", "net.ipv4.ip_forward=1").CombinedOutput(); err != nil {
		log.Printf("[wg] WARNING: failed to enable ip_forward: %s (%v)", strings.TrimSpace(string(out)), err)
	} else {
		log.Printf("[wg] IP forwarding enabled")
	}

	// Add iptables FORWARD rules for the WG interface (idempotent via -C check)
	for _, dir := range []string{"-i", "-o"} {
		// Check if rule exists
		if err := exec.Command("iptables", "-C", "FORWARD", dir, ifaceName, "-j", "ACCEPT").Run(); err == nil {
			continue // rule already exists
		}
		if out, err := exec.Command("iptables", "-A", "FORWARD", dir, ifaceName, "-j", "ACCEPT").CombinedOutput(); err != nil {
			log.Printf("[wg] WARNING: failed to add iptables FORWARD rule (%s %s): %s (%v)", dir, ifaceName, strings.TrimSpace(string(out)), err)
		}
	}
	log.Printf("[wg] iptables FORWARD rules configured for %s", ifaceName)
}
