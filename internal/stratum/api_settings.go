package stratum

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// APIMinerSettings implements MinerSettingsStore by calling the pool API
type APIMinerSettings struct {
	apiURL string
	client *http.Client
}

// NewAPIMinerSettings creates a new API-backed miner settings store
func NewAPIMinerSettings(apiURL string) *APIMinerSettings {
	return &APIMinerSettings{
		apiURL: apiURL,
		client: &http.Client{Timeout: 5 * time.Second},
	}
}

// GetMinerSettings fetches miner settings from the API
func (a *APIMinerSettings) GetMinerSettings(minerID string) (*MinerSettings, error) {
	url := fmt.Sprintf("%s/api/v1/miners/%s/settings", a.apiURL, minerID)
	
	resp, err := a.client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	
	var result struct {
		Exists     bool    `json:"exists"`
		Address    string  `json:"address"`
		SoloMining bool    `json:"solo_mining"`
		ManualDiff float64 `json:"manual_diff"`
		Vardiff    bool    `json:"vardiff"`
	}
	
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	
	// Return settings even if not explicitly configured (defaults)
	return &MinerSettings{
		MinerID:    minerID,
		SoloMining: result.SoloMining,
		ManualDiff: result.ManualDiff,
		Exists:     result.Exists,
	}, nil
}

// SaveMinerSettings saves miner settings via the API (autosave for new miners)
func (a *APIMinerSettings) SaveMinerSettings(settings *MinerSettings) error {
	url := fmt.Sprintf("%s/api/v1/miners/settings", a.apiURL)

	payload := map[string]interface{}{
		"address":     settings.MinerID,
		"solo_mining": settings.SoloMining,
		"manual_diff": settings.ManualDiff,
		"min_payout":  5.0, // Default min payout
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	resp, err := a.client.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("failed to save settings: status %d", resp.StatusCode)
	}

	return nil
}
