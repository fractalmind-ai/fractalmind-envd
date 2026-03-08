package sponsor

import (
	"testing"
	"time"

	internalSui "github.com/fractalmind-ai/fractalmind-envd/internal/sui"
)

func TestIsPackageAllowed_EmptyWhitelist(t *testing.T) {
	s := &Service{
		allowedPackages: map[string]bool{},
	}
	if !s.isPackageAllowed("0xabc") {
		t.Error("empty whitelist should allow all packages")
	}
}

func TestIsPackageAllowed_WithWhitelist(t *testing.T) {
	s := &Service{
		allowedPackages: map[string]bool{
			"0xabc": true,
			"0xdef": true,
		},
	}

	if !s.isPackageAllowed("0xabc") {
		t.Error("whitelisted package should be allowed")
	}
	if !s.isPackageAllowed("0xABC") {
		t.Error("package check should be case-insensitive")
	}
	if s.isPackageAllowed("0x999") {
		t.Error("non-whitelisted package should be rejected")
	}
}

func TestResetDailyIfNeeded(t *testing.T) {
	s := &Service{
		dailyGasUsed: 50_000_000,
		lastResetDay: time.Now().YearDay() - 1, // yesterday
		dailyGasLimit: 100_000_000,
	}

	s.resetDailyIfNeeded()

	if s.dailyGasUsed != 0 {
		t.Errorf("dailyGasUsed = %d, want 0 after reset", s.dailyGasUsed)
	}
	if s.lastResetDay != time.Now().YearDay() {
		t.Error("lastResetDay should be updated to today")
	}
}

func TestResetDailyIfNeeded_SameDay(t *testing.T) {
	s := &Service{
		dailyGasUsed: 50_000_000,
		lastResetDay: time.Now().YearDay(), // today
		dailyGasLimit: 100_000_000,
	}

	s.resetDailyIfNeeded()

	if s.dailyGasUsed != 50_000_000 {
		t.Errorf("dailyGasUsed = %d, want 50000000 (no reset on same day)", s.dailyGasUsed)
	}
}

func TestHandleRequest_ValidationErrors(t *testing.T) {
	s := &Service{
		allowedPackages: map[string]bool{"0xabc": true},
		maxGasPerTx:     10_000_000,
		dailyGasLimit:   100_000_000,
		lastResetDay:    time.Now().YearDay(),
	}

	tests := []struct {
		name string
		req  internalSui.SponsorRequest
		want string
	}{
		{
			name: "empty sender",
			req:  internalSui.SponsorRequest{Module: "peer", Function: "register_peer"},
			want: "sender, module, function are required",
		},
		{
			name: "empty module",
			req:  internalSui.SponsorRequest{Sender: "0x123", Function: "register_peer"},
			want: "sender, module, function are required",
		},
		{
			name: "empty function",
			req:  internalSui.SponsorRequest{Sender: "0x123", Module: "peer"},
			want: "sender, module, function are required",
		},
		{
			name: "package not whitelisted",
			req: internalSui.SponsorRequest{
				Sender: "0x123", PackageID: "0x999",
				Module: "peer", Function: "register_peer",
			},
			want: "package 0x999 not whitelisted",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := s.HandleRequest(nil, tt.req)
			if err == nil {
				t.Fatal("expected error")
			}
			if err.Error() != tt.want {
				t.Errorf("error = %q, want %q", err.Error(), tt.want)
			}
		})
	}
}

func TestHandleRequest_DailyLimitExceeded(t *testing.T) {
	s := &Service{
		allowedPackages: map[string]bool{"0xabc": true},
		maxGasPerTx:     10_000_000,
		dailyGasLimit:   15_000_000, // Only room for 1 TX
		dailyGasUsed:    10_000_000, // Already used 1 TX
		lastResetDay:    time.Now().YearDay(),
	}

	req := internalSui.SponsorRequest{
		Sender:    "0x123",
		PackageID: "0xabc",
		Module:    "peer",
		Function:  "register_peer",
	}

	_, err := s.HandleRequest(nil, req)
	if err == nil {
		t.Fatal("expected daily limit error")
	}
	if got := err.Error(); got != "daily gas limit exceeded (10000000/15000000 MIST)" {
		t.Errorf("error = %q", got)
	}
}

func TestDailyUsage(t *testing.T) {
	s := &Service{
		dailyGasUsed:  42_000_000,
		dailyGasLimit: 100_000_000,
	}

	used, limit := s.DailyUsage()
	if used != 42_000_000 {
		t.Errorf("used = %d, want 42000000", used)
	}
	if limit != 100_000_000 {
		t.Errorf("limit = %d, want 100000000", limit)
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("hello world", 5); got != "hello" {
		t.Errorf("truncate(\"hello world\", 5) = %q, want %q", got, "hello")
	}
	if got := truncate("hi", 5); got != "hi" {
		t.Errorf("truncate(\"hi\", 5) = %q, want %q", got, "hi")
	}
}
