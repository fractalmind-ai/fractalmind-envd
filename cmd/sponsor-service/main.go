package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/block-vision/sui-go-sdk/models"
	suisdk "github.com/block-vision/sui-go-sdk/sui"
	internalSui "github.com/fractalmind-ai/fractalmind-envd/internal/sui"
	"gopkg.in/yaml.v3"
)

// ServiceConfig is the sponsor-service.yaml configuration.
type ServiceConfig struct {
	Listen string    `yaml:"listen"`
	SUI    SUICfg    `yaml:"sui"`
	MaxGas uint64    `yaml:"max_gas_per_tx"`
}

type SUICfg struct {
	RPC               string `yaml:"rpc"`
	KeypairPath       string `yaml:"keypair_path"`
	PackageID         string `yaml:"package_id"`
	SponsorRegistryID string `yaml:"sponsor_registry_id"`
}

// SponsorHandler handles POST /sponsor requests.
type SponsorHandler struct {
	rpc       suisdk.ISuiAPI
	keypair   *internalSui.Keypair
	packageID string
	maxGas    uint64
}

func main() {
	configPath := flag.String("config", "sponsor-service.yaml", "path to config file")
	flag.Parse()

	log.SetPrefix("[sponsor] ")
	log.SetFlags(log.Ldate | log.Ltime | log.Lmsgprefix)

	cfg, err := loadConfig(*configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	kp, err := internalSui.LoadOrGenerateKeypair(cfg.SUI.KeypairPath)
	if err != nil {
		log.Fatalf("load keypair: %v", err)
	}

	rpc := suisdk.NewSuiClient(cfg.SUI.RPC)

	handler := &SponsorHandler{
		rpc:       rpc,
		keypair:   kp,
		packageID: cfg.SUI.PackageID,
		maxGas:    cfg.MaxGas,
	}

	log.Printf("sponsor address: %s", kp.Address())
	log.Printf("whitelisted package: %s", cfg.SUI.PackageID)
	log.Printf("max gas per tx: %d MIST", cfg.MaxGas)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /sponsor", handler.handleSponsor)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	log.Printf("listening on %s", cfg.Listen)
	if err := http.ListenAndServe(cfg.Listen, mux); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

func (h *SponsorHandler) handleSponsor(w http.ResponseWriter, r *http.Request) {
	var req internalSui.SponsorRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Validate required fields
	if req.Sender == "" || req.Module == "" || req.Function == "" {
		writeError(w, http.StatusBadRequest, "sender, module, function are required")
		return
	}

	// Validate package whitelist
	if req.PackageID != h.packageID {
		writeError(w, http.StatusForbidden, fmt.Sprintf("package %s not whitelisted", req.PackageID))
		return
	}

	log.Printf("sponsoring %s::%s for sender %s", req.Module, req.Function, req.Sender[:10])

	// Find a gas coin owned by the sponsor
	gasCoinID, err := h.findGasCoin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("find gas coin: %v", err))
		return
	}

	// Build the TX via MoveCall RPC with sender=worker, gas=sponsor's coin
	// This creates a sponsored TX where gas_owner = sponsor
	gasBudget := strconv.FormatUint(h.maxGas, 10)
	txn, err := h.rpc.MoveCall(r.Context(), models.MoveCallRequest{
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
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("build tx: %v", err))
		return
	}

	// Sign the TX with the sponsor's keypair
	signed := txn.SignSerializedSigWith(ed25519.PrivateKey(h.keypair.Private))

	resp := internalSui.SponsorResponse{
		TxBytes:          signed.TxBytes,
		SponsorSignature: signed.Signature,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)

	log.Printf("sponsored %s::%s for %s (gas_coin=%s)", req.Module, req.Function, req.Sender[:10], gasCoinID[:10])
}

// findGasCoin returns the object ID of a SUI gas coin owned by the sponsor.
func (h *SponsorHandler) findGasCoin(ctx context.Context) (string, error) {
	resp, err := h.rpc.SuiXGetCoins(ctx, models.SuiXGetCoinsRequest{
		Owner:    h.keypair.Address(),
		CoinType: "0x2::sui::SUI",
		Limit:    1,
	})
	if err != nil {
		return "", fmt.Errorf("get coins: %w", err)
	}

	if len(resp.Data) == 0 {
		return "", fmt.Errorf("sponsor has no SUI coins at %s", h.keypair.Address())
	}

	return resp.Data[0].CoinObjectId, nil
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(internalSui.SponsorErrorResponse{Error: msg})
}

func loadConfig(path string) (*ServiceConfig, error) {
	cfg := &ServiceConfig{
		Listen: ":8081",
		SUI: SUICfg{
			RPC:         "https://fullnode.testnet.sui.io:443",
			KeypairPath: "~/.sui/sponsor.key",
		},
		MaxGas: 10000000,
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, err
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, err
	}

	// Validate required fields
	if cfg.SUI.PackageID == "" {
		return nil, fmt.Errorf("sui.package_id is required")
	}

	return cfg, nil
}

// ValidatePackage checks if a package_id is in the whitelist.
// Exported for testing.
func ValidatePackage(packageID, whitelisted string) bool {
	return strings.EqualFold(packageID, whitelisted)
}
