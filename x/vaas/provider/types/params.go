package types

import (
	"fmt"
	"time"

	vaastypes "github.com/allinbits/vaas/x/vaas/types"

	clienttypes "github.com/cosmos/ibc-go/v10/modules/core/02-client/types"
	channeltypesv2 "github.com/cosmos/ibc-go/v10/modules/core/04-channel/v2/types"

	"cosmossdk.io/math"
)

const (
	// DefaultMaxClockDrift defines how much new (untrusted) header's Time can drift into the future.
	// This default is only used in the default template client param.
	DefaultMaxClockDrift = 10 * time.Second

	// DefaultTrustingPeriodFraction is the default fraction used to compute TrustingPeriod
	// as UnbondingPeriod * TrustingPeriodFraction
	DefaultTrustingPeriodFraction = "0.66"

	// DefaultLivenessGraceFraction is the default value of the LivenessGraceFraction
	// param, which sets the consumer liveness grace period as a fraction of the
	// provider unbonding period (grace = unbonding * fraction). A consumer that
	// produces no successful VSC ack for longer than the grace is removed. 0.66
	// mirrors the trusting-period fraction: the grace ends around the client
	// recovery horizon, leaving margin below the unbonding (slashable) window.
	DefaultLivenessGraceFraction = "0.66"

	// DefaultBlocksPerEpoch defines the default blocks that constitute an epoch. Assuming we need 6 seconds per block,
	// an epoch corresponds to 1 hour (6 * 600 = 3600 seconds).
	// forcing int64 as the Params KeyTable expects an int64 and not int.
	DefaultBlocksPerEpoch = int64(600)

	// DefaultFeesPerBlockDenom is the denom charged to each consumer chain per
	// block. It is not a module parameter: it is wired into the keeper at app
	// construction (Keeper.feeDenom) and cannot be changed without a binary
	// upgrade. The atomone hub wires photontypes.Denom here.
	DefaultFeesPerBlockDenom = "uphoton"

	// DefaultFeesPerBlockAmount is the default amount (in DefaultFeesPerBlockDenom) charged per block.
	DefaultFeesPerBlockAmount = int64(1000)

	// DefaultDoubleSignSlashFraction is the default slash fraction for double-sign infractions on consumer chains.
	DefaultDoubleSignSlashFraction = "0.05"

	// DefaultDowntimeSlashFraction is the ceiling on any downtime slash,
	// expressed as a fraction of stake. Downtime slashes are priced from
	// foregone consumer fees (see docs/superpowers/specs/2026-07-14-downtime-evidence-verification-design.md,
	// section 6); this fraction bounds the result.
	DefaultDowntimeSlashFraction = "0.05"

	// DefaultDowntimeGracePeriod is the default grace period after a consumer chain launches
	// during which downtime slashing is suppressed. This gives validators time to spin up
	// their consumer chain nodes without being penalized for early downtime.
	DefaultDowntimeGracePeriod = 7 * 24 * time.Hour // 1 week

	// DefaultDowntimeChallengeWindow is the default duration during which
	// downtime evidence for a given header may still be challenged/disputed
	// before it is considered final.
	DefaultDowntimeChallengeWindow = 7 * 24 * time.Hour

	// DefaultDowntimeEvidenceMaxAge is the default maximum age of a header
	// that downtime evidence may reference.
	DefaultDowntimeEvidenceMaxAge = 72 * time.Hour

	// DefaultMinDepositBlocks is the default minimum-deposit floor expressed
	// as a multiplier of FeesPerBlock.Amount. 14400 blocks is roughly one day
	// at a 6s block time, so the default floor is "one day's worth of fees."
	DefaultMinDepositBlocks = uint64(14400)
)

// NewParams creates new provider parameters with provided arguments
func NewParams(
	trustingPeriodFraction string,
	livenessGraceFraction string,
	vaasTimeoutPeriod time.Duration,
	blocksPerEpoch int64,
	feesPerBlockAmount math.Int,
	minDepositBlocks uint64,
) Params {
	return Params{
		TrustingPeriodFraction: trustingPeriodFraction,
		LivenessGraceFraction:  livenessGraceFraction,
		VaasTimeoutPeriod:      vaasTimeoutPeriod,
		BlocksPerEpoch:         blocksPerEpoch,
		FeesPerBlockAmount:     feesPerBlockAmount,
		MinDepositBlocks:       minDepositBlocks,
	}
}

func DefaultParams() Params {
	return NewParams(
		DefaultTrustingPeriodFraction,
		DefaultLivenessGraceFraction,
		vaastypes.DefaultVAASTimeoutPeriod,
		DefaultBlocksPerEpoch,
		math.NewInt(DefaultFeesPerBlockAmount),
		DefaultMinDepositBlocks,
	)
}

// DefaultInfractionParameters returns the default infraction parameters for consumer chain slashing.
func DefaultInfractionParameters() InfractionParameters {
	doubleSignSlashFraction, _ := math.LegacyNewDecFromStr(DefaultDoubleSignSlashFraction)
	downtimeSlashFraction, _ := math.LegacyNewDecFromStr(DefaultDowntimeSlashFraction)
	minSignedPerWindow, _ := math.LegacyNewDecFromStr(vaastypes.DefaultMinSignedPerWindow)
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
		DowntimeGracePeriod:     DefaultDowntimeGracePeriod,
		SignedBlocksWindow:      vaastypes.DefaultSignedBlocksWindow,
		MinSignedPerWindow:      minSignedPerWindow,
		DowntimeChallengeWindow: DefaultDowntimeChallengeWindow,
		DowntimeEvidenceMaxAge:  DefaultDowntimeEvidenceMaxAge,
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
		SafeModeThreshold: vaastypes.DefaultSafeModeThreshold,
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
	if err := vaastypes.ValidatePositiveInt64(ip.SignedBlocksWindow); err != nil {
		return fmt.Errorf("signed_blocks_window: %s", err)
	}
	if ip.MinSignedPerWindow.IsNil() || !ip.MinSignedPerWindow.IsPositive() || !ip.MinSignedPerWindow.LT(math.LegacyOneDec()) {
		return fmt.Errorf("min_signed_per_window must be in (0, 1), got %s", ip.MinSignedPerWindow)
	}
	if err := vaastypes.ValidateDuration(ip.DowntimeChallengeWindow); err != nil {
		return fmt.Errorf("downtime_challenge_window: %s", err)
	}
	if err := vaastypes.ValidateDuration(ip.DowntimeEvidenceMaxAge); err != nil {
		return fmt.Errorf("downtime_evidence_max_age: %s", err)
	}
	return nil
}

// MaxMissed returns the number of missed blocks in a full window that
// qualifies as a downtime infraction: W - ceil(minSigned * W).
func (ip InfractionParameters) MaxMissed() int64 {
	w := math.LegacyNewDec(ip.SignedBlocksWindow)
	return ip.SignedBlocksWindow - ip.MinSignedPerWindow.Mul(w).Ceil().TruncateInt64()
}

// ValidateInfractionParamsAgainst enforces the cross-param constraint that
// the oldest challengeable header stays light-client verifiable through its
// challenge window: evidenceMaxAge + challengeWindow < trustingFraction *
// defaultConsumerUnbonding. Called wherever incoming infraction params are
// validated together with Params, since InfractionParameters is stored
// separately from Params.
func ValidateInfractionParamsAgainst(ip InfractionParameters, trustingPeriodFraction string) error {
	frac, err := math.LegacyNewDecFromStr(trustingPeriodFraction)
	if err != nil {
		return err
	}
	trusting := time.Duration(frac.MulInt64(int64(vaastypes.DefaultConsumerUnbondingPeriod)).TruncateInt64())
	if ip.DowntimeEvidenceMaxAge+ip.DowntimeChallengeWindow >= trusting {
		return fmt.Errorf(
			"downtime_evidence_max_age + downtime_challenge_window (%s) must be below the default trusting period (%s)",
			ip.DowntimeEvidenceMaxAge+ip.DowntimeChallengeWindow, trusting,
		)
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
	if err := vaastypes.ValidateVAASTimeoutPeriod(p.VaasTimeoutPeriod, channeltypesv2.MaxTimeoutDelta); err != nil {
		return fmt.Errorf("VAAS timeout period is invalid: %s", err)
	}
	if err := vaastypes.ValidatePositiveInt64(p.BlocksPerEpoch); err != nil {
		return fmt.Errorf("blocks per epoch is invalid: %s", err)
	}
	if err := validateFeesPerBlockAmount(p.FeesPerBlockAmount); err != nil {
		return err
	}
	if err := vaastypes.ValidateStringFractionNonZero(p.LivenessGraceFraction); err != nil {
		return fmt.Errorf("liveness grace fraction is invalid: %s", err)
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
