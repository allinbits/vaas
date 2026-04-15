package types

import (
	context "context"
	"time"

	transfertypes "github.com/cosmos/ibc-go/v10/modules/apps/transfer/types"
	clienttypes "github.com/cosmos/ibc-go/v10/modules/core/02-client/types"
	clientv2types "github.com/cosmos/ibc-go/v10/modules/core/02-client/v2/types"
	channeltypesv2 "github.com/cosmos/ibc-go/v10/modules/core/04-channel/v2/types"
	ibcexported "github.com/cosmos/ibc-go/v10/modules/core/exported"

	addresscodec "cosmossdk.io/core/address"
	"cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

// StakingKeeper defines the contract expected by provider-chain ccv module from a Staking Module that will keep track
// of the provider validator set. This version of the interchain-security protocol will mirror the provider chain's changes
// so we do not need a registry module between the staking module and CCV.
type StakingKeeper interface {
	UnbondingCanComplete(ctx context.Context, id uint64) error
	UnbondingTime(ctx context.Context) (time.Duration, error)
	GetValidatorByConsAddr(ctx context.Context, consAddr sdk.ConsAddress) (stakingtypes.Validator, error)
	GetLastValidatorPower(ctx context.Context, operator sdk.ValAddress) (int64, error)
	Jail(context.Context, sdk.ConsAddress) error // jail a validator
	SlashWithInfractionReason(ctx context.Context, consAddr sdk.ConsAddress, infractionHeight, power int64, slashFactor math.LegacyDec, infraction stakingtypes.Infraction) (math.Int, error)
	SlashUnbondingDelegation(ctx context.Context, unbondingDelegation stakingtypes.UnbondingDelegation, infractionHeight int64, slashFactor math.LegacyDec) (math.Int, error)
	SlashRedelegation(ctx context.Context, srcValidator stakingtypes.Validator, redelegation stakingtypes.Redelegation, infractionHeight int64, slashFactor math.LegacyDec) (math.Int, error)
	GetValidator(ctx context.Context, addr sdk.ValAddress) (stakingtypes.Validator, error)
	PowerReduction(ctx context.Context) math.Int
	MaxValidators(ctx context.Context) (uint32, error)
	BondDenom(ctx context.Context) (string, error)
	GetUnbondingDelegationsFromValidator(ctx context.Context, valAddr sdk.ValAddress) ([]stakingtypes.UnbondingDelegation, error)
	GetRedelegationsFromSrcValidator(ctx context.Context, valAddr sdk.ValAddress) ([]stakingtypes.Redelegation, error)
	GetBondedValidatorsByPower(ctx context.Context) ([]stakingtypes.Validator, error)
	IterateDelegations(
		ctx context.Context, delegator sdk.AccAddress,
		fn func(index int64, delegation stakingtypes.DelegationI) (stop bool),
	) error
	IterateBondedValidatorsByPower(
		context.Context, func(index int64, validator stakingtypes.ValidatorI) (stop bool),
	) error
	StakingTokenSupply(ctx context.Context) (math.Int, error)
	BondedRatio(ctx context.Context) (math.LegacyDec, error)
	TotalBondedTokens(ctx context.Context) (math.Int, error)
	GetHistoricalInfo(ctx context.Context, height int64) (stakingtypes.HistoricalInfo, error)
}

// SlashingKeeper defines the contract expected to perform ccv slashing
type SlashingKeeper interface {
	JailUntil(context.Context, sdk.ConsAddress, time.Time) error // called from provider keeper only
	DowntimeJailDuration(context.Context) (time.Duration, error)
	SlashFractionDoubleSign(context.Context) (math.LegacyDec, error)
	Tombstone(context.Context, sdk.ConsAddress) error
	IsTombstoned(context.Context, sdk.ConsAddress) bool
}

// ClientKeeper defines the expected IBC client keeper for v1 operations.
type ClientKeeper interface {
	CreateClient(ctx sdk.Context, clientType string, clientState, consensusState []byte) (string, error)
	GetClientState(ctx sdk.Context, clientID string) (ibcexported.ClientState, bool)
	GetClientConsensusState(ctx sdk.Context, clientID string, height ibcexported.Height) (ibcexported.ConsensusState,
		bool)
	GetClientStatus(ctx sdk.Context, clientID string) ibcexported.Status
	GetStoreProvider() clienttypes.StoreProvider
	IterateClientStates(ctx sdk.Context, storePrefix []byte, cb func(clientID string, cs ibcexported.ClientState) bool)
}

// ClientV2Keeper defines the expected IBC client v2 keeper for counterparty management.
type ClientV2Keeper interface {
	SetClientCounterparty(ctx sdk.Context, clientID string, counterparty clientv2types.CounterpartyInfo)
	GetClientCounterparty(ctx sdk.Context, clientID string) (clientv2types.CounterpartyInfo, bool)
}

// ConsumerHooks event hooks for newly bonded cross-chain validators
type ConsumerHooks interface {
	AfterValidatorBonded(ctx context.Context, consAddr sdk.ConsAddress, valAddresses sdk.ValAddress) error
}

// BankKeeper defines the expected interface needed to retrieve account balances.
type BankKeeper interface {
	GetBalance(ctx context.Context, addr sdk.AccAddress, denom string) sdk.Coin
	SendCoinsFromModuleToModule(ctx context.Context, senderModule, recipientModule string, amt sdk.Coins) error
}

// AccountKeeper defines the expected account keeper used for simulations
type AccountKeeper interface {
	GetModuleAccount(ctx context.Context, name string) sdk.ModuleAccountI
	AddressCodec() addresscodec.Codec
}

// ChannelV2Keeper defines the expected IBC v2 channel keeper for sending packets.
type ChannelV2Keeper interface {
	SendPacket(ctx context.Context, msg *channeltypesv2.MsgSendPacket) (*channeltypesv2.MsgSendPacketResponse, error)
}

// IBCTransferKeeper defines the expected interface needed for distribution transfer
// of tokens from the consumer to the provider chain
type IBCTransferKeeper interface {
	Transfer(context.Context, *transfertypes.MsgTransfer) (*transfertypes.MsgTransferResponse, error)
}
