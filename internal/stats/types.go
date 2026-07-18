package stats

import "time"

// PoolBlock represents a block mined by the pool
type PoolBlock struct {
	Height    int64   `json:"height"`
	Hash      string  `json:"hash"`
	Reward    float64 `json:"reward"`
	MinerAddr string  `json:"miner_address"`
	Status    string  `json:"status"`
	CreatedAt int64   `json:"time"`
	IsSolo    bool    `json:"is_solo"`
}

// MinerBlockContribution represents a miner's contribution to a specific block
type MinerBlockContribution struct {
	Height   int64   `json:"height"`
	Amount   float64 `json:"amount"`
	SharePct float64 `json:"share_pct"`
	Time     int64   `json:"time"`
	IsPaid   bool    `json:"is_paid"`
}

// SoloBlock represents a block found by a solo miner
type SoloBlock struct {
	Height     int64   `json:"height"`
	Hash       string  `json:"hash"`
	Reward     float64 `json:"reward"`
	Time       int64   `json:"time"`
	Status     string  `json:"status"`
	Confirmed  bool    `json:"confirmed"`
	PayoutTxid string  `json:"payoutTxid,omitempty"`
}

// PayoutRecord for API response
type PayoutRecord struct {
	TxID      string    `json:"txid"`
	Amount    float64   `json:"amount"`
	PaidAt    time.Time `json:"paidAt"`
	Blocks    int       `json:"blocks"`
	Confirmed bool      `json:"confirmed"`
}

// MinerSettings represents a miner's pool settings
type MinerSettings struct {
	Address     string  `json:"address"`
	SoloMining  bool    `json:"solo_mining"`
	ManualDiff  float64 `json:"manual_diff"`
	MinPayout   float64 `json:"min_payout"`
	Address1175 string  `json:"address_1175"` // 1175 merge-mining payout address (esf1...)
}
