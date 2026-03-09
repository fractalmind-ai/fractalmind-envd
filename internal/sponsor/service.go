// Package sponsor implements the built-in gas sponsorship service.
// In v3, this runs inside envd (no separate HTTP service).
// Worker nodes send SponsorRequest via WireGuard P2P;
// the sponsor node validates, builds a sponsored TX, co-signs, and returns.
package sponsor

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/block-vision/sui-go-sdk/models"
	suisdk "github.com/block-vision/sui-go-sdk/sui"
	internalSui "github.com/fractalmind-ai/fractalmind-envd/internal/sui"
)

// Service is the built-in sponsor service running inside envd.
type Service struct {
	mu              sync.Mutex
	rpc             suisdk.ISuiAPI
	keypair         *internalSui.Keypair
	allowedPackages map[string]bool
	maxGasPerTx     uint64
	dailyGasLimit   uint64
	dailyGasUsed    uint64
	lastResetDay    int // day of year for daily reset
}

// Config holds sponsor service configuration.
type Config struct {
	SUI_RPC         string
	OrgWalletPath   string
	AllowedPackages []string
	MaxGasPerTx     uint64
	DailyGasLimit   uint64
}

// NewService creates a built-in sponsor service.
func NewService(cfg Config) (*Service, error) {
	kp, err := internalSui.LoadOrGenerateKeypair(cfg.OrgWalletPath)
	if err != nil {
		return nil, fmt.Errorf("load org wallet keypair: %w", err)
	}

	rpc := suisdk.NewSuiClient(cfg.SUI_RPC)

	allowed := make(map[string]bool)
	for _, pkg := range cfg.AllowedPackages {
		allowed[strings.ToLower(pkg)] = true
	}

	maxGas := cfg.MaxGasPerTx
	if maxGas == 0 {
		maxGas = 10_000_000 // 0.01 SUI
	}
	dailyLimit := cfg.DailyGasLimit
	if dailyLimit == 0 {
		dailyLimit = 100_000_000 // 0.1 SUI
	}

	log.Printf("[sponsor] service initialized (address=%s, packages=%d, max_gas=%d, daily_limit=%d)",
		kp.Address(), len(allowed), maxGas, dailyLimit)

	return &Service{
		rpc:             rpc,
		keypair:         kp,
		allowedPackages: allowed,
		maxGasPerTx:     maxGas,
		dailyGasLimit:   dailyLimit,
		lastResetDay:    time.Now().YearDay(),
	}, nil
}

// HandleRequest processes a sponsorship request from a worker node.
// This is the core logic migrated from cmd/sponsor-service.
func (s *Service) HandleRequest(ctx context.Context, req internalSui.SponsorRequest) (*internalSui.SponsorResponse, error) {
	// Validate required fields
	if req.Sender == "" || req.Module == "" || req.Function == "" {
		return nil, fmt.Errorf("sender, module, function are required")
	}

	// Validate package whitelist
	if !s.isPackageAllowed(req.PackageID) {
		return nil, fmt.Errorf("package %s not whitelisted", req.PackageID)
	}

	// Check daily gas limit
	s.mu.Lock()
	s.resetDailyIfNeeded()
	if s.dailyGasUsed+s.maxGasPerTx > s.dailyGasLimit {
		s.mu.Unlock()
		return nil, fmt.Errorf("daily gas limit exceeded (%d/%d MIST)", s.dailyGasUsed, s.dailyGasLimit)
	}
	s.dailyGasUsed += s.maxGasPerTx
	s.mu.Unlock()

	log.Printf("[sponsor] sponsoring %s::%s for sender %s", req.Module, req.Function, truncate(req.Sender, 10))

	// Find a gas coin owned by the sponsor
	gasCoinID, err := s.findGasCoin(ctx)
	if err != nil {
		return nil, fmt.Errorf("find gas coin: %w", err)
	}

	// Build TX with sender=worker, gas=sponsor's coin
	gasBudget := fmt.Sprintf("%d", s.maxGasPerTx)
	txn, err := s.rpc.MoveCall(ctx, models.MoveCallRequest{
		Signer:          req.Sender,
		PackageObjectId: req.PackageID,
		Module:          req.Module,
		Function:        req.Function,
		TypeArguments:   req.TypeArgs,
		Arguments:       req.Args,
		Gas:             &gasCoinID,
		GasBudget:       gasBudget,
	})
	if err != nil {
		return nil, fmt.Errorf("build tx: %w", err)
	}

	// Sign with sponsor's keypair
	signed := txn.SignSerializedSigWith(ed25519.PrivateKey(s.keypair.Private))

	log.Printf("[sponsor] sponsored %s::%s for %s (gas_coin=%s)",
		req.Module, req.Function, truncate(req.Sender, 10), truncate(gasCoinID, 10))

	return &internalSui.SponsorResponse{
		TxBytes:          signed.TxBytes,
		SponsorSignature: signed.Signature,
	}, nil
}

// Address returns the sponsor wallet address.
func (s *Service) Address() string {
	return s.keypair.Address()
}

// TransferGas sends SUI from the sponsor wallet to the given recipient.
// Used to fund worker nodes so they can pay their own gas.
func (s *Service) TransferGas(ctx context.Context, recipient string, amount uint64) error {
	gasCoinID, err := s.findGasCoin(ctx)
	if err != nil {
		return fmt.Errorf("find gas coin: %w", err)
	}

	txn, err := s.rpc.TransferSui(ctx, models.TransferSuiRequest{
		Signer:      s.keypair.Address(),
		SuiObjectId: gasCoinID,
		GasBudget:   "10000000",
		Recipient:   recipient,
		Amount:      fmt.Sprintf("%d", amount),
	})
	if err != nil {
		return fmt.Errorf("build transfer: %w", err)
	}

	signed := txn.SignSerializedSigWith(ed25519.PrivateKey(s.keypair.Private))
	_, err = s.rpc.SuiExecuteTransactionBlock(ctx, models.SuiExecuteTransactionBlockRequest{
		TxBytes:     signed.TxBytes,
		Signature:   []string{signed.Signature},
		Options:     models.SuiTransactionBlockOptions{ShowEffects: true},
		RequestType: "WaitForLocalExecution",
	})
	if err != nil {
		return fmt.Errorf("execute transfer: %w", err)
	}

	log.Printf("[sponsor] transferred %d MIST to %s", amount, truncate(recipient, 10))
	return nil
}

// DailyUsage returns the current daily gas usage.
func (s *Service) DailyUsage() (used, limit uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dailyGasUsed, s.dailyGasLimit
}

func (s *Service) isPackageAllowed(packageID string) bool {
	if len(s.allowedPackages) == 0 {
		return true // no whitelist = allow all
	}
	return s.allowedPackages[strings.ToLower(packageID)]
}

func (s *Service) resetDailyIfNeeded() {
	today := time.Now().YearDay()
	if today != s.lastResetDay {
		s.dailyGasUsed = 0
		s.lastResetDay = today
		log.Printf("[sponsor] daily gas limit reset")
	}
}

func (s *Service) findGasCoin(ctx context.Context) (string, error) {
	resp, err := s.rpc.SuiXGetCoins(ctx, models.SuiXGetCoinsRequest{
		Owner:    s.keypair.Address(),
		CoinType: "0x2::sui::SUI",
		Limit:    1,
	})
	if err != nil {
		return "", fmt.Errorf("get coins: %w", err)
	}

	if len(resp.Data) == 0 {
		return "", fmt.Errorf("sponsor has no SUI coins at %s", s.keypair.Address())
	}

	return resp.Data[0].CoinObjectId, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
