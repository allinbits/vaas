package types

import (
	"fmt"
	"time"

	"cosmossdk.io/math"

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

	// DefaultSignedBlocksWindow is the default number of consumer blocks over
	// which downtime is measured. Provider-owned: written into the consumer
	// genesis and kept in sync via VSC packets.
	DefaultSignedBlocksWindow = int64(600)

	// DefaultMinSignedPerWindow is the default minimum fraction of
	// DefaultSignedBlocksWindow that a validator must sign to avoid a downtime
	// infraction.
	DefaultMinSignedPerWindow = "0.5"
)

// NewConsumerParams creates new consumer parameters with provided arguments.
// The downtime-window fields (SignedBlocksWindow, MinSignedPerWindow) are
// provider-owned and always take their default values here; the provider
// updates them directly on the stored ConsumerParams via VSC packets.
func NewConsumerParams(enabled bool,
	vaasTimeoutPeriod time.Duration,
	historicalEntries int64,
	consumerUnbondingPeriod time.Duration,
	safeModeThreshold time.Duration,
) ConsumerParams {
	return ConsumerParams{
		Enabled:            enabled,
		VaasTimeoutPeriod:  vaasTimeoutPeriod,
		HistoricalEntries:  historicalEntries,
		UnbondingPeriod:    consumerUnbondingPeriod,
		SafeModeThreshold:  safeModeThreshold,
		SignedBlocksWindow: DefaultSignedBlocksWindow,
		MinSignedPerWindow: math.LegacyMustNewDecFromStr(DefaultMinSignedPerWindow),
	}
}

// DefaultConsumerParams is the default params for the consumer module.
func DefaultConsumerParams() ConsumerParams {
	return NewConsumerParams(
		false,
		DefaultVAASTimeoutPeriod,
		DefaultHistoricalEntries,
		DefaultConsumerUnbondingPeriod,
		DefaultSafeModeThreshold,
	)
}

// Validate all VAAS-consumer module parameters
func (p ConsumerParams) Validate() error {
	if err := ValidateDuration(p.VaasTimeoutPeriod); err != nil {
		return err
	}
	if err := ValidatePositiveInt64(p.HistoricalEntries); err != nil {
		return err
	}
	if err := ValidateDuration(p.UnbondingPeriod); err != nil {
		return err
	}
	if err := ValidateDuration(p.SafeModeThreshold); err != nil {
		return err
	}
	if err := ValidatePositiveInt64(p.SignedBlocksWindow); err != nil {
		return fmt.Errorf("signed_blocks_window: %s", err)
	}
	if p.MinSignedPerWindow.IsNil() || !p.MinSignedPerWindow.IsPositive() || !p.MinSignedPerWindow.LT(math.LegacyOneDec()) {
		return fmt.Errorf("min_signed_per_window must be in (0, 1), got %s", p.MinSignedPerWindow)
	}
	return nil
}
