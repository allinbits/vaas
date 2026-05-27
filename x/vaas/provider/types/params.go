package types

import (
	"fmt"
	"time"

	vaastypes "github.com/allinbits/vaas/x/vaas/types"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

const (
	// DefaultMaxClockDrift defines how much new (untrusted) header's Time can drift into the future.
	// This default is only used in the default template client param.
	DefaultMaxClockDrift = 10 * time.Second

	// DefaultTrustingPeriodFraction is the default fraction used to compute TrustingPeriod
	// as UnbondingPeriod * TrustingPeriodFraction
	DefaultTrustingPeriodFraction = "0.66"

	// DefaultBlocksPerEpoch defines the default blocks that constitute an epoch. Assuming we need 6 seconds per block,
	// an epoch corresponds to 1 hour (6 * 600 = 3600 seconds).
	// forcing int64 as the Params KeyTable expects an int64 and not int.
	DefaultBlocksPerEpoch = int64(600)

	// DefaultMaxProviderConsensusValidators is the default maximum number of validators that will
	// be passed on from the staking module to the consensus engine on the provider.
	DefaultMaxProviderConsensusValidators = 180

	// DefaultFeesPerBlockDenom is the base denom charged to each consumer chain per block.
	DefaultFeesPerBlockDenom = "uphoton"

	// DefaultFeesPerBlockAmount is the default amount (in DefaultFeesPerBlockDenom) charged per block.
	DefaultFeesPerBlockAmount = int64(1000)

	// DefaultMinDepositBlocks is the default minimum-deposit floor expressed
	// as a multiplier of FeesPerBlock.Amount. 14400 blocks is roughly one day
	// at a 6s block time, so the default floor is "one day's worth of fees."
	DefaultMinDepositBlocks = uint64(14400)
)

// NewParams creates new provider parameters with provided arguments
func NewParams(
	trustingPeriodFraction string,
	vaasTimeoutPeriod time.Duration,
	blocksPerEpoch int64,
	maxProviderConsensusValidators int64,
	feesPerBlock sdk.Coin,
	minDepositBlocks uint64,
) Params {
	return Params{
		TrustingPeriodFraction:         trustingPeriodFraction,
		VaasTimeoutPeriod:              vaasTimeoutPeriod,
		BlocksPerEpoch:                 blocksPerEpoch,
		MaxProviderConsensusValidators: maxProviderConsensusValidators,
		FeesPerBlock:                   feesPerBlock,
		MinDepositBlocks:               minDepositBlocks,
	}
}

func DefaultParams() Params {
	return NewParams(
		DefaultTrustingPeriodFraction,
		vaastypes.DefaultVAASTimeoutPeriod,
		DefaultBlocksPerEpoch,
		DefaultMaxProviderConsensusValidators,
		sdk.NewInt64Coin(DefaultFeesPerBlockDenom, DefaultFeesPerBlockAmount),
		DefaultMinDepositBlocks,
	)
}

// Validate all VAAS-provider module parameters
func (p Params) Validate() error {
	if err := vaastypes.ValidateStringFractionNonZero(p.TrustingPeriodFraction); err != nil {
		return fmt.Errorf("trusting period fraction is invalid: %s", err)
	}
	if err := vaastypes.ValidateDuration(p.VaasTimeoutPeriod); err != nil {
		return fmt.Errorf("VAAS timeout period is invalid: %s", err)
	}
	if err := vaastypes.ValidatePositiveInt64(p.BlocksPerEpoch); err != nil {
		return fmt.Errorf("blocks per epoch is invalid: %s", err)
	}
	if err := vaastypes.ValidatePositiveInt64(p.MaxProviderConsensusValidators); err != nil {
		return fmt.Errorf("max provider consensus validators is invalid: %s", err)
	}
	if err := validateFeesPerBlock(p.FeesPerBlock); err != nil {
		return err
	}

	return nil
}

func validateFeesPerBlock(coin sdk.Coin) error {
	if !coin.IsValid() {
		return fmt.Errorf("fees per block coin is invalid: %s", coin)
	}
	if coin.IsZero() {
		return fmt.Errorf("fees per block must be positive")
	}
	return nil
}
