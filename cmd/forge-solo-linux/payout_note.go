package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"
)

// payoutAskFor is how long forge-solo run waits for the API to say whether a payout address is
// saved, and payoutAskEvery how often it asks meanwhile.
var (
	payoutAskFor   = 2 * time.Minute
	payoutAskEvery = time.Second
)

// sayPayoutAddress says, once the API answers, that mining waits for a BCH2 payout address when
// none is saved, and nothing when one is. The banner said so at every start, also with one saved.
func sayPayoutAddress(ctx context.Context, api string) {
	actx, cancel := context.WithTimeout(ctx, payoutAskFor)
	defer cancel()
	saved, known := payoutAddressSaved(actx, api)
	if ctx.Err() != nil {
		return
	}
	if note := payoutNote(saved, known); note != "" {
		logf("%s", note)
	}
}

// payoutNote is what forge-solo run says of the payout address: saved, or known not to be, or the
// API did not say.
func payoutNote(saved, known bool) string {
	switch {
	case saved:
		return ""
	case known:
		return "set your BCH2 payout address in the dashboard's Settings: mining waits for it"
	}
	return "if no BCH2 payout address is saved yet, set it in the dashboard's Settings: mining waits for it"
}

// payoutAddressSaved asks the API whether a payout address is saved, as Settings does for its
// "Not configured" banner (pool config: configured), until it answers or ctx ends. known is false
// when it never answered.
func payoutAddressSaved(ctx context.Context, api string) (saved, known bool) {
	client := &http.Client{Timeout: 5 * time.Second}
	for {
		if saved, known = askConfigured(ctx, client, api); known {
			return saved, true
		}
		select {
		case <-ctx.Done():
			return false, false
		case <-time.After(payoutAskEvery):
		}
	}
}

// askConfigured is one question: the API's answer, or known false when it gave none (not up yet,
// or its database not answering).
func askConfigured(ctx context.Context, client *http.Client, api string) (saved, known bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, api+"/api/v1/pool/config", nil)
	if err != nil {
		return false, false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false, false
	}
	defer resp.Body.Close()
	var cfg struct {
		Configured *bool `json:"configured"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&cfg) != nil || cfg.Configured == nil {
		return false, false
	}
	return *cfg.Configured, true
}
