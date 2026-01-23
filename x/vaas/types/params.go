package types

import (
	"time"

	sdktypes "github.com/cosmos/cosmos-sdk/types"
	paramtypes "github.com/cosmos/cosmos-sdk/x/params/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

const (
	// Default number of historical info entries to persist in store.
	// We use the same default as the staking module, but use a signed integer
	// so that negative values can be caught during parameter validation in a readable way,
	// (and for consistency with other protobuf schemas defined for ccv).
	DefaultHistoricalEntries = int64(stakingtypes.DefaultHistoricalEntries)

	// In general, the default unbonding period on the consumer is one day less
	// than the default unbonding period on the provider, where the provider uses
	// the staking module default.
	DefaultConsumerUnbondingPeriod = stakingtypes.DefaultUnbondingTime - 24*time.Hour
)

// Reflection based keys for params subspace
var (
	KeyEnabled                 = []byte("Enabled")
	KeyHistoricalEntries       = []byte("HistoricalEntries")
	KeyConsumerUnbondingPeriod = []byte("UnbondingPeriod")
)

// helper interface
// sdk::paramtypes.ParamSpace implicitly implements this interface because it
// implements the Get(ctx sdk.Context, key []byte, ptr interface{})
// since only Get(...) is needed to migrate params we can ignore the other methods on paramtypes.ParamSpace.
type LegacyParamSubspace interface {
	Get(ctx sdktypes.Context, key []byte, ptr interface{})
}

// ParamKeyTable type declaration for parameters
func ParamKeyTable() paramtypes.KeyTable {
	return paramtypes.NewKeyTable().RegisterParamSet(&ConsumerParams{})
}

// NewParams creates new consumer parameters with provided arguments
func NewParams(enabled bool,
	ccvTimeoutPeriod time.Duration,
	historicalEntries int64,
	consumerUnbondingPeriod time.Duration,
) ConsumerParams {
	return ConsumerParams{
		Enabled:           enabled,
		CcvTimeoutPeriod:  ccvTimeoutPeriod,
		HistoricalEntries: historicalEntries,
		UnbondingPeriod:   consumerUnbondingPeriod,
	}
}

// DefaultParams is the default params for the consumer module
func DefaultParams() ConsumerParams {
	return NewParams(
		false,
		DefaultCCVTimeoutPeriod,
		DefaultHistoricalEntries,
		DefaultConsumerUnbondingPeriod,
	)
}

// Validate all ccv-consumer module parameters
func (p ConsumerParams) Validate() error {
	if err := ValidateBool(p.Enabled); err != nil {
		return err
	}
	if err := ValidateDuration(p.CcvTimeoutPeriod); err != nil {
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

// ParamSetPairs implements params.ParamSet
func (p *ConsumerParams) ParamSetPairs() paramtypes.ParamSetPairs {
	return paramtypes.ParamSetPairs{
		paramtypes.NewParamSetPair(KeyEnabled, p.Enabled, ValidateBool),
		paramtypes.NewParamSetPair(KeyCCVTimeoutPeriod,
			p.CcvTimeoutPeriod, ValidateDuration),
		paramtypes.NewParamSetPair(KeyHistoricalEntries,
			p.HistoricalEntries, ValidatePositiveInt64),
		paramtypes.NewParamSetPair(KeyConsumerUnbondingPeriod,
			p.UnbondingPeriod, ValidateDuration),
	}
}

