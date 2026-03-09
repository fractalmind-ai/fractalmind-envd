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
