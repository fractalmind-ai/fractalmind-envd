package sui

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/crypto/blake2b"
)

// Keypair holds an Ed25519 keypair for SUI transactions.
type Keypair struct {
	Private ed25519.PrivateKey
	Public  ed25519.PublicKey
}

// LoadOrGenerateKeypair loads an Ed25519 keypair from path, or generates a new
// one and writes it to disk with 0600 permissions.
func LoadOrGenerateKeypair(path string) (*Keypair, error) {
	// Expand ~ to home directory
	if len(path) > 0 && path[0] == '~' {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("expand home dir: %w", err)
		}
		path = filepath.Join(home, path[1:])
	}

	data, err := os.ReadFile(path)
	if err == nil {
		return loadKeypair(data)
	}
	if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read keypair: %w", err)
	}

	// Generate new keypair
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate keypair: %w", err)
	}

	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, fmt.Errorf("create keypair dir: %w", err)
	}

	// Write private key as hex
	if err := os.WriteFile(path, []byte(hex.EncodeToString(priv)), 0600); err != nil {
		return nil, fmt.Errorf("write keypair: %w", err)
	}

	return &Keypair{Private: priv, Public: pub}, nil
}

func loadKeypair(data []byte) (*Keypair, error) {
	decoded, err := hex.DecodeString(string(data))
	if err != nil {
		return nil, fmt.Errorf("decode keypair hex: %w", err)
	}

	if len(decoded) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("invalid private key size: got %d, want %d", len(decoded), ed25519.PrivateKeySize)
	}

	priv := ed25519.PrivateKey(decoded)
	pub := priv.Public().(ed25519.PublicKey)
	return &Keypair{Private: priv, Public: pub}, nil
}

// Address returns the SUI address derived from the public key.
// SUI address = BLAKE2b-256(0x00 || pubkey)[0:32] as hex with 0x prefix.
// The 0x00 flag byte indicates Ed25519 scheme.
func (kp *Keypair) Address() string {
	// SUI address: BLAKE2b-256(scheme_flag || pubkey_bytes)
	payload := make([]byte, 1+len(kp.Public))
	payload[0] = 0x00 // Ed25519 scheme flag
	copy(payload[1:], kp.Public)

	hash := blake2b.Sum256(payload)
	return "0x" + hex.EncodeToString(hash[:])
}

// Sign signs data with the Ed25519 private key.
// Implements the relay.Signer interface.
func (kp *Keypair) Sign(data []byte) []byte {
	return ed25519.Sign(kp.Private, data)
}

// PublicKeyBytes returns the raw Ed25519 public key bytes.
// Implements the relay.Signer interface.
func (kp *Keypair) PublicKeyBytes() []byte {
	return []byte(kp.Public)
}
