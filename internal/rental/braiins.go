package rental

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
)

// BraiinsClient handles Braiins Hashpower API interactions
type BraiinsClient struct {
	apiKey     string
	apiURL     string
	httpClient *http.Client
}

// NewBraiinsClient creates a new Braiins API client with secure TLS configuration
func NewBraiinsClient(apiKey string) *BraiinsClient {
	// Configure TLS with minimum version and secure settings
	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS12, // Require TLS 1.2 or higher
		CipherSuites: []uint16{
			// Modern, secure cipher suites only
			tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305,
			tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305,
		},
	}

	return &BraiinsClient{
		apiKey: apiKey,
		apiURL: "https://hashpower.braiins.com/webapi",
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: tlsConfig,
			},
		},
	}
}

// doRequest performs an API request with authentication
func (c *BraiinsClient) doRequest(method, endpoint string, body interface{}) ([]byte, error) {
	var reqBody io.Reader
	if body != nil {
		jsonData, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal request: %w", err)
		}
		reqBody = bytes.NewBuffer(jsonData)
	}

	req, err := http.NewRequest(method, c.apiURL+endpoint, reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("apikey", c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		// Log full error for debugging but return sanitized message
		// Response body may contain sensitive API information
		log.Printf("Braiins API error (status %d): %s", resp.StatusCode, string(respBody))
		return nil, fmt.Errorf("hashrate marketplace API error (status %d)", resp.StatusCode)
	}

	return respBody, nil
}

// GetBalance fetches the account balance
func (c *BraiinsClient) GetBalance() (*BraiinsBalance, error) {
	respBody, err := c.doRequest("GET", "/account/balance", nil)
	if err != nil {
		return nil, err
	}

	var response struct {
		Accounts []struct {
			TotalBalanceSat     int64 `json:"total_balance_sat"`
			AvailableBalanceSat int64 `json:"available_balance_sat"`
			BlockedBalanceSat   int64 `json:"blocked_balance_sat"`
		} `json:"accounts"`
	}

	if err := json.Unmarshal(respBody, &response); err != nil {
		return nil, fmt.Errorf("failed to decode balance: %w", err)
	}

	if len(response.Accounts) == 0 {
		return &BraiinsBalance{}, nil
	}

	return &BraiinsBalance{
		TotalSat:     response.Accounts[0].TotalBalanceSat,
		AvailableSat: response.Accounts[0].AvailableBalanceSat,
		BlockedSat:   response.Accounts[0].BlockedBalanceSat,
	}, nil
}

// GetOrderbook fetches the current orderbook
func (c *BraiinsClient) GetOrderbook() (*BraiinsOrderbook, error) {
	respBody, err := c.doRequest("GET", "/orderbook", nil)
	if err != nil {
		return nil, err
	}

	var response struct {
		Asks []struct {
			PriceSat          int64   `json:"price_sat"`
			HashRateAvailable float64 `json:"hashRateAvailable"`
		} `json:"asks"`
		Bids []struct {
			PriceSat int64 `json:"price_sat"`
		} `json:"bids"`
	}

	if err := json.Unmarshal(respBody, &response); err != nil {
		return nil, fmt.Errorf("failed to decode orderbook: %w", err)
	}

	result := &BraiinsOrderbook{}

	if len(response.Asks) > 0 {
		result.BestAskSat = response.Asks[0].PriceSat
		// Sum available hashrate
		for _, ask := range response.Asks {
			result.AvailablePH += ask.HashRateAvailable
		}
	}

	if len(response.Bids) > 0 {
		result.BestBidSat = response.Bids[0].PriceSat
	}

	return result, nil
}

// PlaceBidRequest represents a bid placement request
type PlaceBidRequest struct {
	DestUpstream struct {
		URL      string `json:"url"`
		Identity string `json:"identity"`
	} `json:"dest_upstream"`
	AmountSat    int64   `json:"amount_sat"`
	PriceSat     int64   `json:"price_sat"`
	SpeedLimitPH float64 `json:"speed_limit_ph"`
	Memo         string  `json:"memo,omitempty"`
}

// PlaceBidResponse represents the response from placing a bid
type PlaceBidResponse struct {
	ID        string `json:"id"`
	ClOrderID string `json:"cl_order_id"`
}

// PlaceBidWithClientID places a new bid on the Braiins market with a client order ID
func (c *BraiinsClient) PlaceBidWithClientID(poolURL, identity string, amountSat, priceSat int64, speedLimitPH float64, memo, clOrderID string) (string, error) {
	request := map[string]interface{}{
		"dest_upstream": map[string]string{
			"url":      poolURL,
			"identity": identity,
		},
		"amount_sat":     amountSat,
		"price_sat":      priceSat,
		"speed_limit_ph": speedLimitPH,
		"memo":           memo,
	}

	if clOrderID != "" {
		request["cl_order_id"] = clOrderID
	}

	log.Printf("Braiins PlaceBid: amount=%d, price=%d, speed=%.2f, cl_order_id=%s", amountSat, priceSat, speedLimitPH, clOrderID)

	respBody, err := c.doRequest("POST", "/spot/bid", request)
	if err != nil {
		return "", err
	}

	var response struct {
		ID    string `json:"id"`
		Error string `json:"error,omitempty"`
	}

	if err := json.Unmarshal(respBody, &response); err != nil {
		return "", fmt.Errorf("failed to decode response: %w", err)
	}

	if response.Error != "" {
		return "", fmt.Errorf("braiins error: %s", response.Error)
	}

	// Validate required response fields
	if response.ID == "" {
		return "", fmt.Errorf("braiins API returned empty bid ID")
	}

	return response.ID, nil
}

// PlaceBid places a new bid on the Braiins market (backward compatible)
func (c *BraiinsClient) PlaceBid(poolURL, identity string, amountSat, priceSat int64, speedLimitPH float64, memo string) (string, error) {
	return c.PlaceBidWithClientID(poolURL, identity, amountSat, priceSat, speedLimitPH, memo, "")
}

// CancelBid cancels an existing bid
func (c *BraiinsClient) CancelBid(orderID string) error {
	request := map[string]string{
		"order_id": orderID,
	}

	respBody, err := c.doRequest("DELETE", "/spot/bid", request)
	if err != nil {
		return err
	}

	var response struct {
		Success bool   `json:"success"`
		Error   string `json:"error,omitempty"`
	}

	if err := json.Unmarshal(respBody, &response); err != nil {
		// Some responses don't have JSON body
		return nil
	}

	if response.Error != "" {
		return fmt.Errorf("cancel error: %s", response.Error)
	}

	return nil
}

// GetCurrentBids fetches all active bids
func (c *BraiinsClient) GetCurrentBids() ([]BraiinsBid, error) {
	respBody, err := c.doRequest("GET", "/spot/bid/current", nil)
	if err != nil {
		return nil, err
	}

	var response struct {
		Bids []struct {
			Bid struct {
				ID           string  `json:"id"`
				Status       string  `json:"status"`
				PriceSat     int64   `json:"price_sat"`
				AmountSat    int64   `json:"amount_sat"`
				SpeedLimitPH float64 `json:"speed_limit_ph"`
			} `json:"bid"`
			StateEstimate struct {
				AvgSpeedPH   float64 `json:"avg_speed_ph"`
				ProgressPct  float64 `json:"progress_pct"`
			} `json:"state_estimate"`
			CountersEstimate struct {
				AmountConsumedSat int64 `json:"amount_consumed_sat"`
			} `json:"counters_estimate"`
		} `json:"bids"`
	}

	if err := json.Unmarshal(respBody, &response); err != nil {
		return nil, fmt.Errorf("failed to decode bids: %w", err)
	}

	var bids []BraiinsBid
	for _, b := range response.Bids {
		bids = append(bids, BraiinsBid{
			ID:             b.Bid.ID,
			Status:         b.Bid.Status,
			PriceSat:       b.Bid.PriceSat,
			AmountSat:      b.Bid.AmountSat,
			SpeedLimitPH:   b.Bid.SpeedLimitPH,
			AvgSpeedPH:     b.StateEstimate.AvgSpeedPH,
			ProgressPct:    b.StateEstimate.ProgressPct,
			AmountSpentSat: b.CountersEstimate.AmountConsumedSat,
		})
	}

	return bids, nil
}

// GetCompletedBids fetches historical bids that are no longer active (cancelled or fulfilled)
func (c *BraiinsClient) GetCompletedBids(limit int) ([]BraiinsBid, error) {
	endpoint := fmt.Sprintf("/spot/bid?limit=%d&exclude_active=true", limit)
	respBody, err := c.doRequest("GET", endpoint, nil)
	if err != nil {
		return nil, err
	}

	var response struct {
		Bids []struct {
			Bid struct {
				ID           string  `json:"id"`
				ClOrderID    string  `json:"cl_order_id"`
				Status       string  `json:"status"`
				PriceSat     int64   `json:"price_sat"`
				AmountSat    int64   `json:"amount_sat"`
				SpeedLimitPH float64 `json:"speed_limit_ph"`
				Memo         string  `json:"memo"`
			} `json:"bid"`
			CountersCommitted struct {
				AmountConsumedSat int64 `json:"amount_consumed_sat"`
				FeePaidSat        int64 `json:"fee_paid_sat"`
			} `json:"counters_committed"`
		} `json:"bids"`
	}

	if err := json.Unmarshal(respBody, &response); err != nil {
		return nil, fmt.Errorf("failed to decode completed bids: %w", err)
	}

	var bids []BraiinsBid
	for _, b := range response.Bids {
		bids = append(bids, BraiinsBid{
			ID:             b.Bid.ID,
			ClOrderID:      b.Bid.ClOrderID,
			Status:         b.Bid.Status,
			PriceSat:       b.Bid.PriceSat,
			AmountSat:      b.Bid.AmountSat,
			SpeedLimitPH:   b.Bid.SpeedLimitPH,
			Memo:           b.Bid.Memo,
			AmountSpentSat: b.CountersCommitted.AmountConsumedSat,
			FeePaidSat:     b.CountersCommitted.FeePaidSat,
		})
	}

	return bids, nil
}

// GetBidDetail fetches detailed info for a specific bid
func (c *BraiinsClient) GetBidDetail(orderID string) (*BraiinsBid, error) {
	respBody, err := c.doRequest("GET", "/spot/bid/detail/"+orderID, nil)
	if err != nil {
		return nil, err
	}

	var response struct {
		Bid struct {
			ID              string  `json:"id"`
			Status          string  `json:"status"`
			PriceSat        int64   `json:"price_sat"`
			AmountSat       int64   `json:"amount_sat"`
			SpeedLimitPH    float64 `json:"speed_limit_ph"`
			LastPauseReason string  `json:"last_pause_reason"`
		} `json:"bid"`
		StateEstimate struct {
			AvgSpeedPH      float64 `json:"avg_speed_ph"`
			ProgressPct     float64 `json:"progress_pct"`
			AmountRemaining int64   `json:"amount_remaining_sat"`
		} `json:"state_estimate"`
		CountersEstimate struct {
			AmountConsumedSat int64 `json:"amount_consumed_sat"`
		} `json:"counters_estimate"`
	}

	if err := json.Unmarshal(respBody, &response); err != nil {
		return nil, fmt.Errorf("failed to decode bid detail: %w", err)
	}

	// Validate that we received a valid bid
	if response.Bid.ID == "" {
		return nil, fmt.Errorf("braiins API returned empty bid ID for order %s", orderID)
	}

	return &BraiinsBid{
		ID:              response.Bid.ID,
		Status:          response.Bid.Status,
		PriceSat:        response.Bid.PriceSat,
		AmountSat:       response.Bid.AmountSat,
		SpeedLimitPH:    response.Bid.SpeedLimitPH,
		AvgSpeedPH:      response.StateEstimate.AvgSpeedPH,
		ProgressPct:     response.StateEstimate.ProgressPct,
		AmountSpentSat:  response.CountersEstimate.AmountConsumedSat,
		AmountRemaining: response.StateEstimate.AmountRemaining,
		LastPauseReason: response.Bid.LastPauseReason,
	}, nil
}

// GetMarketSettings fetches market parameters
func (c *BraiinsClient) GetMarketSettings() (map[string]interface{}, error) {
	respBody, err := c.doRequest("GET", "/spot/settings", nil)
	if err != nil {
		return nil, err
	}

	var result map[string]interface{}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("failed to decode settings: %w", err)
	}

	return result, nil
}

// CalculateBidPrice calculates the bid price with margin
// Returns the price aligned to tick size
func (c *BraiinsClient) CalculateBidPrice(marginPct float64) (int64, error) {
	orderbook, err := c.GetOrderbook()
	if err != nil {
		return 0, err
	}

	if orderbook.BestAskSat == 0 {
		return 0, fmt.Errorf("no asks available")
	}

	// Add margin and round to tick size (1000 sats)
	priceWithMargin := float64(orderbook.BestAskSat) * (1 + marginPct/100)
	tickSize := int64(1000)
	alignedPrice := ((int64(priceWithMargin) + tickSize - 1) / tickSize) * tickSize

	return alignedPrice, nil
}
