package keeper_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	sdk "github.com/cosmos/cosmos-sdk/types"

	cryptotestutil "github.com/allinbits/vaas/testutil/crypto"
	testkeeper "github.com/allinbits/vaas/testutil/keeper"
	providerkeeper "github.com/allinbits/vaas/x/vaas/provider/keeper"
	"github.com/allinbits/vaas/x/vaas/provider/types"
)

func TestValidatorConsensusKeyInUse(t *testing.T) {
	newValidator := cryptotestutil.NewCryptoIdentityFromIntSeed(0)
	anotherValidator0 := cryptotestutil.NewCryptoIdentityFromIntSeed(1)
	anotherValidator1 := cryptotestutil.NewCryptoIdentityFromIntSeed(2)

	tests := []struct {
		name   string
		setup  func(sdk.Context, providerkeeper.Keeper)
		expect bool
	}{
		{
			name: "not in use by another validator",
			setup: func(ctx sdk.Context, k providerkeeper.Keeper) {
				// Another validator does not exist
			},
			expect: false,
		},
		{
			name: "in use by another validator",
			setup: func(ctx sdk.Context, k providerkeeper.Keeper) {
				k.FetchAndIncrementConsumerId(ctx)
				k.SetConsumerPhase(ctx, 0, types.CONSUMER_PHASE_INITIALIZED)

				// We are trying to add a new validator, but its address has already been used
				// by another validator
				k.SetValidatorByConsumerAddr(ctx, 0,
					newValidator.ConsumerConsAddress(),
					anotherValidator0.ProviderConsAddress(),
				)
				k.SetConsumerClientId(ctx, 0, "clientId")
			},
			expect: true,
		},
		{
			name: "in use by one of several other validators",
			setup: func(ctx sdk.Context, k providerkeeper.Keeper) {
				k.FetchAndIncrementConsumerId(ctx)
				k.FetchAndIncrementConsumerId(ctx)
				k.SetConsumerPhase(ctx, 0, types.CONSUMER_PHASE_INITIALIZED)
				k.SetConsumerPhase(ctx, 1, types.CONSUMER_PHASE_INITIALIZED)

				// We are trying to add a new validator, but its address has already been used
				// by another validator, of which there are several, across potentially several chains
				k.SetValidatorByConsumerAddr(ctx, 0,
					newValidator.ConsumerConsAddress(),
					anotherValidator0.ProviderConsAddress(),
				)
				k.SetConsumerClientId(ctx, 0, "clientId0")

				k.SetValidatorByConsumerAddr(ctx, 1,
					anotherValidator1.ConsumerConsAddress(),
					anotherValidator1.ProviderConsAddress(),
				)
				k.SetConsumerClientId(ctx, 1, "clientId1")
			},
			expect: true,
		},
	}
	for _, tt := range tests {
		k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
		defer ctrl.Finish()

		gomock.InOrder(
			mocks.MockStakingKeeper.EXPECT().GetValidator(ctx,
				newValidator.SDKValOpAddress(),
			).Return(newValidator.SDKStakingValidator(), nil),
		)

		tt.setup(ctx, k)

		t.Run(tt.name, func(t *testing.T) {
			if actual := k.ValidatorConsensusKeyInUse(ctx, newValidator.SDKValOpAddress()); actual != tt.expect {
				t.Errorf("validatorConsensusKeyInUse() = %v, want %v", actual, tt.expect)
			}
		})
	}
}

// TestAfterValidatorCreatedRefusesAConsensusKeyAssignedAsAConsumerKey: the
// hook aborts the creation of a validator whose consensus key another
// validator holds as an assigned consumer key on a consumer that is not
// deleted, since the two would share one consensus address on that consumer;
// a free key, or one held only on a deleted consumer, passes.
func TestAfterValidatorCreatedRefusesAConsensusKeyAssignedAsAConsumerKey(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	created := cryptotestutil.NewCryptoIdentityFromIntSeed(0)
	holder := cryptotestutil.NewCryptoIdentityFromIntSeed(1)
	mocks.MockStakingKeeper.EXPECT().GetValidator(gomock.Any(), created.SDKValOpAddress()).
		Return(created.SDKStakingValidator(), nil).AnyTimes()
	create := func() error {
		return k.Hooks().AfterValidatorCreated(ctx, created.SDKValOpAddress())
	}

	consumerID := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, consumerID, types.CONSUMER_PHASE_LAUNCHED)
	require.NoError(t, create(), "a consensus key nobody holds as a consumer key passes")

	k.SetValidatorByConsumerAddr(ctx, consumerID, created.ConsumerConsAddress(), holder.ProviderConsAddress())
	for _, phase := range []types.ConsumerPhase{types.CONSUMER_PHASE_LAUNCHED, types.CONSUMER_PHASE_PAUSED, types.CONSUMER_PHASE_STOPPED} {
		k.SetConsumerPhase(ctx, consumerID, phase)
		require.Panics(t, func() { _ = create() }, "a %s consumer keeps its assignments", phase)
	}

	k.SetConsumerPhase(ctx, consumerID, types.CONSUMER_PHASE_DELETED)
	require.NoError(t, create(), "a deleted consumer's assignments count no more")
}

// TestAfterValidatorRemovedClearsTheValidatorKeyAssignmentsOnEveryConsumer:
// when a validator leaves the staking set, both directions of its key
// assignment are erased on every consumer it assigned a key to, and the
// assignments of every other validator are left in place.
func TestAfterValidatorRemovedClearsTheValidatorKeyAssignmentsOnEveryConsumer(t *testing.T) {
	k, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	first := k.FetchAndIncrementConsumerId(ctx)
	second := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, first, types.CONSUMER_PHASE_LAUNCHED)
	k.SetConsumerPhase(ctx, second, types.CONSUMER_PHASE_LAUNCHED)

	assign := func(consumerID uint64, validator, consumerKey *cryptotestutil.CryptoIdentity) {
		k.SetValidatorConsumerPubKey(ctx, consumerID, validator.ProviderConsAddress(), consumerKey.TMProtoCryptoPublicKey())
		k.SetValidatorByConsumerAddr(ctx, consumerID, consumerKey.ConsumerConsAddress(), validator.ProviderConsAddress())
	}
	assigned := func(consumerID uint64, validator, consumerKey *cryptotestutil.CryptoIdentity) bool {
		_, byProvider := k.GetValidatorConsumerPubKey(ctx, consumerID, validator.ProviderConsAddress())
		_, byConsumer := k.GetValidatorByConsumerAddr(ctx, consumerID, consumerKey.ConsumerConsAddress())
		require.Equal(t, byProvider, byConsumer, "the two directions of an assignment must agree")
		return byProvider
	}

	removed := cryptotestutil.NewCryptoIdentityFromIntSeed(1)
	removedKeyOnFirst := cryptotestutil.NewCryptoIdentityFromIntSeed(10)
	removedKeyOnSecond := cryptotestutil.NewCryptoIdentityFromIntSeed(11)
	assign(first, removed, removedKeyOnFirst)
	assign(second, removed, removedKeyOnSecond)

	staying := cryptotestutil.NewCryptoIdentityFromIntSeed(2)
	stayingKey := cryptotestutil.NewCryptoIdentityFromIntSeed(12)
	assign(first, staying, stayingKey)

	require.NoError(t, k.Hooks().AfterValidatorRemoved(ctx, removed.SDKValConsAddress(), removed.SDKValOpAddress()))

	require.False(t, assigned(first, removed, removedKeyOnFirst), "the removed validator's assignment on the first consumer must be gone")
	require.False(t, assigned(second, removed, removedKeyOnSecond), "the removed validator's assignment on the second consumer must be gone")
	require.True(t, assigned(first, staying, stayingKey), "another validator's assignment must be left in place")

	require.NoError(t, k.Hooks().AfterValidatorRemoved(ctx, removed.SDKValConsAddress(), removed.SDKValOpAddress()),
		"removing a validator with nothing assigned is a no-op")
	require.True(t, assigned(first, staying, stayingKey))
}
