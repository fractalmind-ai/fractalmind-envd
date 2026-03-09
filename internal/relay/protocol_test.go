package relay

import (
	"encoding/binary"
	"testing"
)

func TestEncodeDecodeDataMsg(t *testing.T) {
	tests := []struct {
		name     string
		peerID   uint16
		wgPacket []byte
	}{
		{"basic", 1, []byte("hello wireguard")},
		{"zero peer", 0, []byte{0x01, 0x02, 0x03}},
		{"max peer", 65535, []byte{0xff}},
		{"large packet", 42, make([]byte, 1400)}, // typical WG MTU
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded := EncodeDataMsg(tt.peerID, tt.wgPacket)

			// Verify length: 2-byte header + payload
			if len(encoded) != 2+len(tt.wgPacket) {
				t.Fatalf("encoded length = %d, want %d", len(encoded), 2+len(tt.wgPacket))
			}

			// Verify peer ID in header
			gotID := binary.BigEndian.Uint16(encoded[:2])
			if gotID != tt.peerID {
				t.Errorf("header peer ID = %d, want %d", gotID, tt.peerID)
			}

			// Decode and verify roundtrip
			decID, decPacket, err := DecodeDataMsg(encoded)
			if err != nil {
				t.Fatalf("DecodeDataMsg() error: %v", err)
			}
			if decID != tt.peerID {
				t.Errorf("decoded peer ID = %d, want %d", decID, tt.peerID)
			}
			if len(decPacket) != len(tt.wgPacket) {
				t.Fatalf("decoded packet length = %d, want %d", len(decPacket), len(tt.wgPacket))
			}
			for i := range tt.wgPacket {
				if decPacket[i] != tt.wgPacket[i] {
					t.Errorf("decoded packet[%d] = %d, want %d", i, decPacket[i], tt.wgPacket[i])
					break
				}
			}
		})
	}
}

func TestDecodeDataMsg_TooShort(t *testing.T) {
	tests := []struct {
		name string
		msg  []byte
	}{
		{"empty", []byte{}},
		{"one byte", []byte{0x01}},
		{"header only", []byte{0x00, 0x01}}, // 2 bytes = header but no data
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := DecodeDataMsg(tt.msg)
			if err == nil {
				t.Error("DecodeDataMsg() should return error for too-short message")
			}
		})
	}
}

func TestEncodeDataMsg_IsolatesInput(t *testing.T) {
	// Verify that modifying the input after encoding doesn't affect the encoded message
	peerID := uint16(7)
	original := []byte{0x01, 0x02, 0x03}
	encoded := EncodeDataMsg(peerID, original)

	// Mutate original
	original[0] = 0xff

	// Decoded should still have original value
	_, decoded, err := DecodeDataMsg(encoded)
	if err != nil {
		t.Fatalf("DecodeDataMsg() error: %v", err)
	}
	if decoded[0] != 0x01 {
		t.Errorf("decoded[0] = %d, want 1 (mutation leaked)", decoded[0])
	}
}
