package types

import (
	"fmt"
	"time"

	vaastypes "github.com/allinbits/vaas/x/vaas/types"

	clienttypes "github.com/cosmos/ibc-go/v10/modules/core/02-client/types"

	"cosmossdk.io/math"
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

	// DefaultFeesPerBlockDenom is the denom charged to each consumer chain per
	// block. It is not a module parameter: it is wired into the keeper at app
	// construction (Keeper.feeDenom) and cannot be changed without a binary
	// upgrade. The atomone hub wires photontypes.Denom here.
	DefaultFeesPerBlockDenom = "uphoton"

	// DefaultFeesPerBlockAmount is the default amount (in DefaultFeesPerBlockDenom) charged per block.
	DefaultFeesPerBlockAmount = int64(1000)

	// DefaultDoubleSignSlashFraction is the default slash fraction for double-sign infractions on consumer chains.
	DefaultDoubleSignSlashFraction = "0.05"

	// DefaultDowntimeSlashFraction is the default slash fraction for downtime infractions on consumer chains (0.05%).
	DefaultDowntimeSlashFraction = "0.0005"

	// DefaultDowntimeGracePeriod is the default grace period after a consumer chain launches
	// during which downtime slashing is suppressed. This gives validators time to spin up
	// their consumer chain nodes without being penalized for early downtime.
	DefaultDowntimeGracePeriod = 7 * 24 * time.Hour // 1 week

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
	feesPerBlockAmount math.Int,
	minDepositBlocks uint64,
) Params {
	return Params{
		TrustingPeriodFraction:         trustingPeriodFraction,
		VaasTimeoutPeriod:              vaasTimeoutPeriod,
		BlocksPerEpoch:                 blocksPerEpoch,
		MaxProviderConsensusValidators: maxProviderConsensusValidators,
		FeesPerBlockAmount:             feesPerBlockAmount,
		MinDepositBlocks:               minDepositBlocks,
	}
}

func DefaultParams() Params {
	return NewParams(
		DefaultTrustingPeriodFraction,
		vaastypes.DefaultVAASTimeoutPeriod,
		DefaultBlocksPerEpoch,
		DefaultMaxProviderConsensusValidators,
		math.NewInt(DefaultFeesPerBlockAmount),
		DefaultMinDepositBlocks,
	)
}

// DefaultInfractionParameters returns the default infraction parameters for consumer chain slashing.
func DefaultInfractionParameters() InfractionParameters {
	doubleSignSlashFraction, _ := math.LegacyNewDecFromStr(DefaultDoubleSignSlashFraction)
	downtimeSlashFraction, _ := math.LegacyNewDecFromStr(DefaultDowntimeSlashFraction)
	return InfractionParameters{
		DoubleSign: &SlashJailParameters{
			JailDuration:  time.Duration(1<<63 - 1),
			SlashFraction: doubleSignSlashFraction,
			Tombstone:     true,
		},
		Downtime: &SlashJailParameters{
			JailDuration:  0,
			SlashFraction: downtimeSlashFraction,
			Tombstone:     false,
		},
		DowntimeGracePeriod: DefaultDowntimeGracePeriod,
	}
}

func DefaultConsumerInitializationParameters() ConsumerInitializationParameters {
	return ConsumerInitializationParameters{
		InitialHeight: clienttypes.Height{
			RevisionNumber: 1,
			RevisionHeight: 1,
		},
		GenesisHash:       []byte{},
		BinaryHash:        []byte{},
		SpawnTime:         time.Time{},
		UnbondingPeriod:   vaastypes.DefaultConsumerUnbondingPeriod,
		VaasTimeoutPeriod: vaastypes.DefaultVAASTimeoutPeriod,
		HistoricalEntries: vaastypes.DefaultHistoricalEntries,
	}
}

// Validate performs basic validation of infraction parameters.
func (ip InfractionParameters) Validate() error {
	if ip.DoubleSign == nil {
		return fmt.Errorf("double_sign infraction parameters must be set")
	}
	if err := ip.DoubleSign.Validate(); err != nil {
		return fmt.Errorf("double_sign: %s", err)
	}
	if ip.Downtime == nil {
		return fmt.Errorf("downtime infraction parameters must be set")
	}
	if err := ip.Downtime.Validate(); err != nil {
		return fmt.Errorf("downtime: %s", err)
	}
	if ip.DowntimeGracePeriod < 0 {
		return fmt.Errorf("downtime_grace_period must not be negative")
	}
	return nil
}

// Validate performs basic validation of slash/jail parameters.
func (sjp SlashJailParameters) Validate() error {
	if err := vaastypes.ValidateFraction(sjp.SlashFraction); err != nil {
		return fmt.Errorf("slash_fraction: %s", err)
	}
	if sjp.JailDuration < 0 {
		return fmt.Errorf("jail_duration must not be negative")
	}
	return nil
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
	if err := validateFeesPerBlockAmount(p.FeesPerBlockAmount); err != nil {
		return err
	}

	return nil
}

func validateFeesPerBlockAmount(amount math.Int) error {
	if amount.IsNil() {
		return fmt.Errorf("fees per block amount must be set")
	}
	if !amount.IsPositive() {
		return fmt.Errorf("fees per block amount must be positive")
	}
	return nil
}
