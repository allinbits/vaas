package types

import (
	"time"

	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

const (
	// Default number of historical info entries to persist in store.
	// We use the same default as the staking module, but use a signed integer
	// so that negative values can be caught during parameter validation in a readable way,
	// (and for consistency with other protobuf schemas defined for VAAS).
	DefaultHistoricalEntries = int64(stakingtypes.DefaultHistoricalEntries)

	// In general, the default unbonding period on the consumer is one day less
	// than the default unbonding period on the provider, where the provider uses
	// the staking module default.
	DefaultConsumerUnbondingPeriod = stakingtypes.DefaultUnbondingTime - 24*time.Hour
)

// NewParams creates new consumer parameters with provided arguments
func NewParams(enabled bool,
	vaasTimeoutPeriod time.Duration,
	historicalEntries int64,
	consumerUnbondingPeriod time.Duration,
) ConsumerParams {
	return ConsumerParams{
		Enabled:           enabled,
		VaasTimeoutPeriod: vaasTimeoutPeriod,
		HistoricalEntries: historicalEntries,
		UnbondingPeriod:   consumerUnbondingPeriod,
	}
}

// DefaultParams is the default params for the consumer module
func DefaultParams() ConsumerParams {
	return NewParams(
		false,
		DefaultVAASTimeoutPeriod,
		DefaultHistoricalEntries,
		DefaultConsumerUnbondingPeriod,
	)
}

// Validate all VAAS-consumer module parameters
func (p ConsumerParams) Validate() error {
	if err := ValidateBool(p.Enabled); err != nil {
		return err
	}
	if err := ValidateDuration(p.VaasTimeoutPeriod); err != nil {
		return err
	}
	if err := ValidatePositiveInt64(p.HistoricalEntries); err != nil {
		return err
	}
	if err := ValidateDuration(p.UnbondingPeriod); err != nil {
		return err
	}
	return nil
}
