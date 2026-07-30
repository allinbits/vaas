package keeper

import (
	"context"

	providertypes "github.com/allinbits/vaas/x/vaas/provider/types"
	vaastypes "github.com/allinbits/vaas/x/vaas/types"

	"cosmossdk.io/math"

	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkgov "github.com/cosmos/cosmos-sdk/x/gov/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

// Hooks wrapper struct
type Hooks struct {
	k *Keeper
}

var (
	_ stakingtypes.StakingHooks = Hooks{}
	_ sdkgov.GovHooks           = Hooks{}
)

// Hooks returns new provider hooks
func (k *Keeper) Hooks() Hooks {
	return Hooks{k}
}

//
// staking hooks
//

func (h Hooks) AfterUnbondingInitiated(goCtx context.Context, id uint64) error {
	return nil
}

func (h Hooks) AfterValidatorCreated(goCtx context.Context, valAddr sdk.ValAddress) error {
	ctx := sdk.UnwrapSDKContext(goCtx)

	if h.k.ValidatorConsensusKeyInUse(ctx, valAddr) {
		// Abort TX, do NOT allow validator to be created
		panic("cannot create a validator with a consensus key that is already in use or was recently in use as an assigned consumer chain key")
	}
	return nil
}

func (h Hooks) AfterValidatorRemoved(goCtx context.Context, valConsAddr sdk.ConsAddress, valAddr sdk.ValAddress) error {
	ctx := sdk.UnwrapSDKContext(goCtx)

	for _, validatorConsumerPubKey := range h.k.GetAllValidatorConsumerPubKeys(ctx, nil) {
		if sdk.ConsAddress(validatorConsumerPubKey.ProviderAddr).Equals(valConsAddr) {
			consumerAddrTmp, err := vaastypes.TMCryptoPublicKeyToConsAddr(*validatorConsumerPubKey.ConsumerKey)
			if err != nil {
				// An error here would indicate something is very wrong
				panic("cannot get address of consumer key")
			}
			consumerAddr := providertypes.NewConsumerConsAddress(consumerAddrTmp)
			h.k.DeleteValidatorByConsumerAddr(ctx, validatorConsumerPubKey.ConsumerId, consumerAddr)
			providerAddr := providertypes.NewProviderConsAddress(validatorConsumerPubKey.ProviderAddr)
			h.k.DeleteValidatorConsumerPubKey(ctx, validatorConsumerPubKey.ConsumerId, providerAddr)
		}
	}

	return nil
}

func (h Hooks) BeforeDelegationCreated(_ context.Context, _ sdk.AccAddress, _ sdk.ValAddress) error {
	return nil
}

func (h Hooks) BeforeDelegationSharesModified(_ context.Context, _ sdk.AccAddress, _ sdk.ValAddress) error {
	return nil
}

func (h Hooks) AfterDelegationModified(_ context.Context, _ sdk.AccAddress, _ sdk.ValAddress) error {
	return nil
}

func (h Hooks) BeforeValidatorSlashed(_ context.Context, _ sdk.ValAddress, _ math.LegacyDec) error {
	return nil
}

func (h Hooks) BeforeValidatorModified(_ context.Context, _ sdk.ValAddress) error {
	return nil
}

func (h Hooks) AfterValidatorBonded(_ context.Context, _ sdk.ConsAddress, _ sdk.ValAddress) error {
	return nil
}

func (h Hooks) AfterValidatorBeginUnbonding(_ context.Context, _ sdk.ConsAddress, _ sdk.ValAddress) error {
	return nil
}

func (h Hooks) BeforeDelegationRemoved(_ context.Context, _ sdk.AccAddress, _ sdk.ValAddress) error {
	return nil
}

func (h Hooks) BeforeTokenizeShareRecordRemoved(_ context.Context, _ uint64) error {
	return nil
}

// AfterConsensusPubKeyUpdate fires when a provider validator rotates its
// consensus key. VAAS state that is keyed by the provider consensus address is
// migrated to the new address (MigrateStateOnConsPubKeyRotation), and every
// consumer whose view of the validator the rotation changes is handed the new
// key right away instead of at the next epoch boundary
// (QueueConsPubKeyRotationSnapshots).
//
// The hook runs in EndBlock -- x/staking applies a recorded rotation from
// ApplyAndReturnValidatorSetUpdates, not from the MsgRotateConsPubKey handler --
// so by the time it is called the rotation is already committed and there is
// nothing left to reject. Any error returned here would propagate out of
// EndBlock and halt the provider chain, so it always returns nil, and both
// steps log what they cannot do rather than reporting it upwards.
//
// Rotating onto a consensus key already assigned as some validator's consumer
// key is therefore refused at tx admission instead, by the ante decorator in
// x/vaas/provider/ante. If such a rotation lands anyway -- on a chain that did
// not wire that decorator -- it is logged here, and the consumer validator set
// computation drops the colliding entry so neither chain is handed a set with
// two validators at one consensus address (see CreateConsumerValidators).
func (h Hooks) AfterConsensusPubKeyUpdate(goCtx context.Context, oldPk, newPk cryptotypes.PubKey, _ sdk.Coin) error {
	ctx := sdk.UnwrapSDKContext(goCtx)
	oldAddr := providertypes.NewProviderConsAddress(sdk.ConsAddress(oldPk.Address()))
	newAddr := providertypes.NewProviderConsAddress(sdk.ConsAddress(newPk.Address()))

	if h.k.IsConsumerConsAddrInUse(ctx, newAddr.ToSdkConsAddr()) {
		h.k.Logger(ctx).Error(
			"validator rotated its provider consensus key onto a key already assigned as a consumer key",
			"providerConsAddr", newAddr.String(),
		)
	}

	h.k.MigrateStateOnConsPubKeyRotation(ctx, oldAddr, newAddr)
	h.k.QueueConsPubKeyRotationSnapshots(ctx, newAddr)
	return nil
}

//
// gov hooks
//

func (h Hooks) AfterProposalSubmission(goCtx context.Context, proposalId uint64) error {
	return nil
}

func (h Hooks) AfterProposalVotingPeriodEnded(goCtx context.Context, proposalId uint64) error {
	return nil
}

func (h Hooks) AfterProposalDeposit(ctx context.Context, proposalID uint64, depositorAddr sdk.AccAddress) error {
	return nil
}

func (h Hooks) AfterProposalVote(ctx context.Context, proposalID uint64, voterAddr sdk.AccAddress) error {
	return nil
}

func (h Hooks) AfterProposalFailedMinDeposit(ctx context.Context, proposalID uint64) error {
	return nil
}
