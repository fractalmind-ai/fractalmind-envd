package sui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// SponsorRequest is the request sent to the Sponsor Service.
type SponsorRequest struct {
	Sender    string        `json:"sender"`
	PackageID string        `json:"package_id"`
	Module    string        `json:"module"`
	Function  string        `json:"function"`
	TypeArgs  []interface{} `json:"type_args"`
	Args      []interface{} `json:"args"`
}

// SponsorResponse is the response from the Sponsor Service.
type SponsorResponse struct {
	TxBytes          string `json:"tx_bytes"`
	SponsorSignature string `json:"sponsor_signature"`
}

// SponsorErrorResponse is an error response from the Sponsor Service.
type SponsorErrorResponse struct {
	Error string `json:"error"`
}

// SponsorProvider abstracts sponsorship — either local (in-process) or remote (HTTP).
type SponsorProvider interface {
	RequestSponsorship(ctx context.Context, req SponsorRequest) (*SponsorResponse, error)
}

// SponsorClient is an HTTP client for the Sponsor Service.
type SponsorClient struct {
	url    string
	client *http.Client
}

// NewSponsorClient creates a Sponsor Service HTTP client.
func NewSponsorClient(url string) *SponsorClient {
	return &SponsorClient{
		url: url,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// RequestSponsorship sends a move call intent to the Sponsor Service and
// receives back the sponsored tx_bytes and sponsor's signature.
func (sc *SponsorClient) RequestSponsorship(ctx context.Context, req SponsorRequest) (*SponsorResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, sc.url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := sc.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var errResp SponsorErrorResponse
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Error != "" {
			return nil, fmt.Errorf("sponsor service: %s", errResp.Error)
		}
		return nil, fmt.Errorf("sponsor service returned %d: %s", resp.StatusCode, string(respBody))
	}

	var result SponsorResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	if result.TxBytes == "" || result.SponsorSignature == "" {
		return nil, fmt.Errorf("sponsor service returned incomplete response")
	}

	return &result, nil
}
