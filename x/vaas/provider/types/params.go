package types

import (
	"fmt"
	"time"

	vaastypes "github.com/allinbits/vaas/x/vaas/types"

	clienttypes "github.com/cosmos/ibc-go/v10/modules/core/02-client/types"
	commitmenttypes "github.com/cosmos/ibc-go/v10/modules/core/23-commitment/types"
	ibctmtypes "github.com/cosmos/ibc-go/v10/modules/light-clients/07-tendermint"

	paramtypes "github.com/cosmos/cosmos-sdk/x/params/types"
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
)

// Reflection based keys for params subspace
// Legacy: usage of x/params for parameters is deprecated.
// Use x/vaas/provider/keeper/params instead
// [DEPRECATED]
var (
	KeyTemplateClient                 = []byte("TemplateClient")
	KeyTrustingPeriodFraction         = []byte("TrustingPeriodFraction")
	KeyBlocksPerEpoch                 = []byte("BlocksPerEpoch")
	KeyMaxProviderConsensusValidators = []byte("MaxProviderConsensusValidators")
)

// ParamKeyTable returns a key table with the necessary registered provider params
func ParamKeyTable() paramtypes.KeyTable {
	return paramtypes.NewKeyTable().RegisterParamSet(&Params{})
}

// NewParams creates new provider parameters with provided arguments
func NewParams(
	cs *ibctmtypes.ClientState,
	trustingPeriodFraction string,
	vaasTimeoutPeriod time.Duration,
	blocksPerEpoch int64,
	maxProviderConsensusValidators int64,
) Params {
	return Params{
		TemplateClient:                 cs,
		TrustingPeriodFraction:         trustingPeriodFraction,
		VaasTimeoutPeriod:              vaasTimeoutPeriod,
		BlocksPerEpoch:                 blocksPerEpoch,
		MaxProviderConsensusValidators: maxProviderConsensusValidators,
	}
}

// DefaultTemplateClient is the default template client
func DefaultTemplateClient() *ibctmtypes.ClientState {
	return ibctmtypes.NewClientState(
		"", // chainID
		ibctmtypes.DefaultTrustLevel,
		0, // trusting period
		0, // unbonding period
		DefaultMaxClockDrift,
		clienttypes.Height{}, // latest(initial) height
		commitmenttypes.GetSDKSpecs(),
		[]string{"upgrade", "upgradedIBCState"},
	)
}

// DefaultParams is the default params for the provider module
func DefaultParams() Params {
	// create default client state with chainID, trusting period, unbonding period, and initial height zeroed out.
	// these fields will be populated during proposal handler.
	return NewParams(
		DefaultTemplateClient(),
		DefaultTrustingPeriodFraction,
		vaastypes.DefaultVAASTimeoutPeriod,
		DefaultBlocksPerEpoch,
		DefaultMaxProviderConsensusValidators,
	)
}

// Validate all VAAS-provider module parameters
func (p Params) Validate() error {
	if p.TemplateClient == nil {
		return fmt.Errorf("template client is nil")
	}
	if err := ValidateTemplateClient(*p.TemplateClient); err != nil {
		return err
	}
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
	return nil
}

// ParamSetPairs implements params.ParamSet
func (p *Params) ParamSetPairs() paramtypes.ParamSetPairs {
	return paramtypes.ParamSetPairs{
		paramtypes.NewParamSetPair(KeyTemplateClient, p.TemplateClient, ValidateTemplateClient),
		paramtypes.NewParamSetPair(KeyTrustingPeriodFraction, p.TrustingPeriodFraction, vaastypes.ValidateStringFraction),
		paramtypes.NewParamSetPair(vaastypes.KeyVAASTimeoutPeriod, p.VaasTimeoutPeriod, vaastypes.ValidateDuration),
		paramtypes.NewParamSetPair(KeyBlocksPerEpoch, p.BlocksPerEpoch, vaastypes.ValidatePositiveInt64),
		paramtypes.NewParamSetPair(KeyMaxProviderConsensusValidators, p.MaxProviderConsensusValidators, vaastypes.ValidatePositiveInt64),
	}
}

func ValidateTemplateClient(i interface{}) error {
	cs, ok := i.(ibctmtypes.ClientState)
	if !ok {
		return fmt.Errorf("invalid parameter type: %T, expected: %T", i, ibctmtypes.ClientState{})
	}

	// copy clientstate to prevent changing original pointer
	copiedClient := cs

	// populate zeroed fields with valid fields
	copiedClient.ChainId = "chainid"

	trustPeriod, err := vaastypes.CalculateTrustPeriod(vaastypes.DefaultConsumerUnbondingPeriod, DefaultTrustingPeriodFraction)
	if err != nil {
		return fmt.Errorf("invalid TrustPeriodFraction: %T", err)
	}
	copiedClient.TrustingPeriod = trustPeriod

	copiedClient.UnbondingPeriod = vaastypes.DefaultConsumerUnbondingPeriod
	copiedClient.LatestHeight = clienttypes.NewHeight(0, 1)

	if err := copiedClient.Validate(); err != nil {
		return err
	}
	return nil
}
