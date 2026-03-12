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
// Rules are restricted to the 10.87.0.0/16 mesh subnet for defense-in-depth.
// All operations are idempotent.
func EnableIPForward(ifaceName string) {
	// Enable IP forwarding
	if out, err := exec.Command("sysctl", "-w", "net.ipv4.ip_forward=1").CombinedOutput(); err != nil {
		log.Printf("[wg] WARNING: failed to enable ip_forward: %s (%v)", strings.TrimSpace(string(out)), err)
	} else {
		log.Printf("[wg] IP forwarding enabled")
	}

	// Add iptables FORWARD rules restricted to mesh subnet (idempotent via -C check).
	// Only allow forwarding of traffic within the 10.87.0.0/16 VPN subnet
	// through the WG interface — prevents the relay from becoming an open router.
	const meshSubnet = "10.87.0.0/16"
	rules := [][6]string{
		{"-i", ifaceName, "-s", meshSubnet, "-d", meshSubnet},
		{"-o", ifaceName, "-s", meshSubnet, "-d", meshSubnet},
	}
	for _, r := range rules {
		args := []string{"-C", "FORWARD", r[0], r[1], r[2], r[3], r[4], r[5], "-j", "ACCEPT"}
		if err := exec.Command("iptables", args...).Run(); err == nil {
			continue // rule already exists
		}
		args[0] = "-A"
		if out, err := exec.Command("iptables", args...).CombinedOutput(); err != nil {
			log.Printf("[wg] WARNING: failed to add iptables FORWARD rule (%s %s %s %s): %s (%v)",
				r[0], r[1], r[2]+"/"+r[4], r[5], strings.TrimSpace(string(out)), err)
		}
	}
	log.Printf("[wg] iptables FORWARD rules configured for %s (subnet %s)", ifaceName, meshSubnet)
}
