//go:build darwin

package wg

import (
	"fmt"
	"log"
	"os/exec"
	"strings"
)

// ensureInterface creates a WireGuard interface on macOS using wireguard-go.
//
// macOS differences from Linux:
//   - No kernel WireGuard module; uses wireguard-go (userspace) instead
//   - Interface names must match utun[0-9]* format (e.g., utun99)
//   - wireguard-go creates and manages the utun device
//   - Requires root/sudo for TUN device creation
//   - wgctrl communicates via /var/run/wireguard/<name>.sock
func ensureInterface(name string) error {
	// Check if interface already exists via ifconfig
	if out, err := exec.Command("ifconfig", name).CombinedOutput(); err == nil {
		_ = out
		return nil
	}

	// Validate interface name for macOS
	if !strings.HasPrefix(name, "utun") {
		log.Printf("[wg] WARNING: macOS requires utun* interface names (got %q). "+
			"Set wireguard.interface_name to e.g. \"utun99\" in your config.", name)
		return fmt.Errorf("macOS requires utun* interface names, got %q", name)
	}

	// Use wireguard-go to create the userspace interface
	if out, err := exec.Command("wireguard-go", name).CombinedOutput(); err != nil {
		return fmt.Errorf("wireguard-go %s: %s (%w)", name, string(out), err)
	}

	log.Printf("[wg] created interface %s (via wireguard-go)", name)
	return nil
}

// assignInterfaceAddr assigns a CIDR address to a network interface on macOS.
// Uses ifconfig to set a point-to-point address and adds a route for the /16 subnet.
func assignInterfaceAddr(name, cidr string) error {
	// Parse IP from CIDR
	ip := cidr
	if idx := strings.Index(cidr, "/"); idx >= 0 {
		ip = cidr[:idx]
	}

	// Check if already assigned
	out, err := exec.Command("ifconfig", name).CombinedOutput()
	if err == nil && strings.Contains(string(out), ip) {
		return nil
	}

	// Set point-to-point address: ifconfig <name> inet <ip> <ip> netmask 255.255.0.0
	if out, err := exec.Command("ifconfig", name, "inet", ip, ip, "netmask", "255.255.0.0").CombinedOutput(); err != nil {
		return fmt.Errorf("ifconfig %s inet %s: %s (%w)", name, ip, string(out), err)
	}

	// Add route for the 10.87.0.0/16 subnet
	if out, err := exec.Command("route", "add", "-net", "10.87.0.0/16", "-interface", name).CombinedOutput(); err != nil {
		// Route may already exist; log but don't fail
		log.Printf("[wg] route add warning: %s", string(out))
	}

	return nil
}
