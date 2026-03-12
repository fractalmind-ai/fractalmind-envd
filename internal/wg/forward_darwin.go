//go:build darwin

package wg

// EnableIPForward is a no-op on macOS.
// IP forwarding configuration on macOS relay nodes is not supported.
func EnableIPForward(_ string) {}
