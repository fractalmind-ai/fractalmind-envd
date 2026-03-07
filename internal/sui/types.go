package sui

// PeerInfo represents a peer node discovered from SUI events.
type PeerInfo struct {
	Address        string
	OrgID          string
	WireGuardPubKey []byte
	Endpoints      []string
	Hostname       string
	Status         uint8
}

const (
	PeerStatusOnline  uint8 = 0
	PeerStatusOffline uint8 = 1
)
