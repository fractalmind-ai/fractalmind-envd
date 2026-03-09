package stun

import (
	"testing"
)

func TestDiscoverEndpoint_NoServers(t *testing.T) {
	_, err := DiscoverEndpoint(nil, "")
	if err == nil {
		t.Error("expected error with no servers")
	}
}

func TestDiscoverEndpoint_AllFail(t *testing.T) {
	// Use invalid addresses that will fail to dial
	_, err := DiscoverEndpoint([]string{"stun:127.0.0.1:1"}, "")
	if err == nil {
		t.Error("expected error when all servers fail")
	}
}
