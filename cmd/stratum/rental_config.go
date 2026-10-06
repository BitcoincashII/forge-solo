package main

import (
	"github.com/BitcoincashII/forge-solo/internal/stratum"
	"github.com/spf13/viper"
)

// rentalServerConfig is the rental port's listener, from the stratum_rental section of config.
//
// variance_percent is read as the main port reads its own: a percentage in the config, a fraction
// here. The rental port read none, so stratum_rental.vardiff.variance_percent did nothing. Every
// platform ships it unset, and unset (0) the listener keeps stratum.VardiffVariancePercent, 30%.
func rentalServerConfig(config *viper.Viper) *stratum.ServerConfig {
	return &stratum.ServerConfig{
		Host:                config.GetString("stratum_rental.host"),
		Port:                config.GetInt("stratum_rental.port"),
		MaxConnections:      config.GetInt("stratum_rental.max_connections"),
		MaxConnectionsPerIP: perIPLimit(config, "stratum_rental.max_connections_per_ip", config.GetInt("stratum_rental.max_connections")),
		MaxSharesPerSecond:  config.GetInt("stratum_rental.max_shares_per_second"),
		VardiffEnabled:      config.GetBool("stratum_rental.vardiff.enabled"),
		MinDiff:             config.GetFloat64("stratum_rental.vardiff.min_diff"),
		VariancePercent:     config.GetFloat64("stratum_rental.vardiff.variance_percent") / 100.0,
		MaxDiff:             config.GetFloat64("stratum_rental.vardiff.max_diff"),
		TargetShareTime:     config.GetInt("stratum_rental.vardiff.target_time"),
		RetargetTime:        config.GetInt("stratum_rental.vardiff.retarget_time"),
		ExtraNonce1Size:     config.GetInt("stratum_rental.extranonce1_size"),
		ExtraNonce2Size:     config.GetInt("stratum_rental.extranonce2_size"),
		ServerName:          "rental",
		IsRentalPort:        true,
		SoloOnly:            config.GetString("pool.payout_scheme") == "solo",
		CreditPayoutAddress: config.GetString("pool.payout_scheme") == "solo",
	}
}
