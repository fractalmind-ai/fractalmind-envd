//go:build linux

package wg

import (
	"fmt"
	"log"
	"os/exec"
)

// ensureInterface creates a kernel WireGuard interface if it does not exist.
// On Linux, this uses iproute2 commands (ip link add/set).
func ensureInterface(name string) error {
	// Check if interface already exists
	if out, err := exec.Command("ip", "link", "show", name).CombinedOutput(); err == nil {
		_ = out
		return nil
	}

	if out, err := exec.Command("ip", "link", "add", "dev", name, "type", "wireguard").CombinedOutput(); err != nil {
		return fmt.Errorf("ip link add %s: %s (%w)", name, string(out), err)
	}
	if out, err := exec.Command("ip", "link", "set", name, "up").CombinedOutput(); err != nil {
		return fmt.Errorf("ip link set %s up: %s (%w)", name, string(out), err)
	}
	log.Printf("[wg] created interface %s", name)
	return nil
}
