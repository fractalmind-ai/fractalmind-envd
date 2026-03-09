package stun

import (
	"fmt"
	"log"
	"net"
	"strings"
	"time"

	stunlib "github.com/pion/stun/v3"
)

// LayeredDiscover tries STUN servers in priority order:
//  1. Org STUN servers (same org, discovered on-chain)
//  2. Shared STUN servers (cross-org, discovered on-chain)
//  3. Public STUN servers (Google/Cloudflare, from config)
//
// Returns the first successful endpoint discovery.
// bindAddr is optional; if non-empty, STUN probes bind to this local address.
func LayeredDiscover(orgServers, sharedServers, publicServers []string, bindAddr string) (string, error) {
	if len(orgServers) > 0 {
		endpoint, err := DiscoverEndpoint(orgServers, bindAddr)
		if err == nil {
			log.Printf("[stun] resolved via org STUN")
			return endpoint, nil
		}
		log.Printf("[stun] org STUN failed, trying shared: %v", err)
	}

	if len(sharedServers) > 0 {
		endpoint, err := DiscoverEndpoint(sharedServers, bindAddr)
		if err == nil {
			log.Printf("[stun] resolved via shared STUN")
			return endpoint, nil
		}
		log.Printf("[stun] shared STUN failed, trying public: %v", err)
	}

	return DiscoverEndpoint(publicServers, bindAddr)
}

// DiscoverEndpoint uses STUN to discover the public IP:port of this host.
// Tries servers in order, returns the first successful result.
// bindAddr is optional; if non-empty, STUN probes bind to this local address.
func DiscoverEndpoint(servers []string, bindAddr string) (string, error) {
	for _, server := range servers {
		addr := strings.TrimPrefix(server, "stun:")
		endpoint, err := stunQuery(addr, bindAddr)
		if err != nil {
			log.Printf("[stun] server %s failed: %v", addr, err)
			continue
		}
		log.Printf("[stun] discovered endpoint: %s", endpoint)
		return endpoint, nil
	}
	return "", fmt.Errorf("all STUN servers failed")
}

func stunQuery(server, bindAddr string) (string, error) {
	var conn net.Conn
	var err error

	if bindAddr != "" {
		// Bind to specific local address (avoids VPN tunnel routing issues)
		dialer := net.Dialer{
			Timeout:   5 * time.Second,
			LocalAddr: &net.UDPAddr{IP: net.ParseIP(bindAddr)},
		}
		conn, err = dialer.Dial("udp", server)
	} else {
		conn, err = net.DialTimeout("udp", server, 5*time.Second)
	}
	if err != nil {
		return "", fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(5 * time.Second))

	msg := stunlib.New()
	msg.SetType(stunlib.BindingRequest)
	msg.NewTransactionID()
	msg.Encode()

	if _, err := conn.Write(msg.Raw); err != nil {
		return "", fmt.Errorf("write: %w", err)
	}

	buf := make([]byte, 1024)
	n, err := conn.Read(buf)
	if err != nil {
		return "", fmt.Errorf("read: %w", err)
	}

	resp := stunlib.New()
	resp.Raw = buf[:n]
	if err := resp.Decode(); err != nil {
		return "", fmt.Errorf("decode: %w", err)
	}

	var xorAddr stunlib.XORMappedAddress
	if err := xorAddr.GetFrom(resp); err != nil {
		var mappedAddr stunlib.MappedAddress
		if err := mappedAddr.GetFrom(resp); err != nil {
			return "", fmt.Errorf("no address in response: %w", err)
		}
		return fmt.Sprintf("%s:%d", mappedAddr.IP, mappedAddr.Port), nil
	}

	return fmt.Sprintf("%s:%d", xorAddr.IP, xorAddr.Port), nil
}
