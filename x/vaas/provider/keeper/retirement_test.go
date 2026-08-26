package keeper_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	clienttypes "github.com/cosmos/ibc-go/v10/modules/core/02-client/types"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"

	cryptotestutil "github.com/allinbits/vaas/testutil/crypto"
	testkeeper "github.com/allinbits/vaas/testutil/keeper"
	providerkeeper "github.com/allinbits/vaas/x/vaas/provider/keeper"
	providertypes "github.com/allinbits/vaas/x/vaas/provider/types"
	vaastypes "github.com/allinbits/vaas/x/vaas/types"
)

// retirementInitParams returns initialization parameters whose initial-height
// revision matches a "-1"-suffixed chain id, with the spawn time supplied by
// the caller (zero leaves the consumer registered, a future time initializes
// it and enqueues it for launch).
func retirementInitParams(spawnTime time.Time) providertypes.ConsumerInitializationParameters {
	ip := providertypes.DefaultConsumerInitializationParameters()
	ip.InitialHeight = clienttypes.Height{RevisionNumber: 1, RevisionHeight: 1}
	ip.SpawnTime = spawnTime
	return ip
}

// createRetirableConsumer registers a consumer owned by owner and returns its
// consumer id. A non-zero spawnTime leaves it in the initialized phase, queued
// for launch; a zero one leaves it registered.
func createRetirableConsumer(
	t *testing.T, k providerkeeper.Keeper, ctx sdk.Context,
	owner, chainId string, spawnTime time.Time,
) uint64 {
	t.Helper()

	ip := retirementInitParams(spawnTime)
	ms := providerkeeper.NewMsgServerImpl(&k)
	resp, err := ms.CreateConsumer(ctx, &providertypes.MsgCreateConsumer{
		Submitter: owner,
		ChainId:   chainId,
		Metadata: providertypes.ConsumerMetadata{
			Name: "retirable", Description: "description", Metadata: "metadata",
		},
		InitializationParameters: &ip,
	})
	require.NoError(t, err)
	return resp.ConsumerId
}

// requireConsumerTornDown asserts that nothing is left of a retired consumer
// beyond the tombstone DeleteConsumerChain keeps on purpose (phase, owner,
// metadata and initialization parameters, for explorers).
func requireConsumerTornDown(
	t *testing.T, k providerkeeper.Keeper, ctx sdk.Context, consumerId uint64,
) {
	t.Helper()

	require.Equal(t, providertypes.CONSUMER_PHASE_DELETED, k.GetConsumerPhase(ctx, consumerId))

	_, err := k.GetConsumerChainId(ctx, consumerId)
	require.ErrorIs(t, err, collections.ErrNotFound, "chain id must be released")

	_, found := k.GetConsumerClientId(ctx, consumerId)
	require.False(t, found, "client id must be deleted")

	_, err = k.FeePoolAddressToConsumerId.Get(ctx, k.GetConsumerFeePoolAddress(consumerId))
	require.ErrorIs(t, err, collections.ErrNotFound, "fee-pool reverse lookup must be deleted")

	require.Empty(t, k.GetAllValidatorConsumerPubKeys(ctx, &consumerId), "key assignments must be cleared")
	require.Empty(t, k.GetAllValidatorsByConsumerAddr(ctx, &consumerId), "consumer-addr lookups must be cleared")
	require.Empty(t, k.GetAllConsumerAddrsToPrune(ctx, consumerId), "addresses to prune must be cleared")

	_, found = k.GetConsumerGenesis(ctx, consumerId)
	require.False(t, found, "consumer genesis must be deleted")
	_, found = k.GetInitChainHeight(ctx, consumerId)
	require.False(t, found, "init chain height must be deleted")
	require.Zero(t, k.GetEquivocationEvidenceMinHeight(ctx, consumerId))
	require.Empty(t, k.GetPendingVSCPackets(ctx, consumerId), "pending VSC packets must be deleted")

	valSet, err := k.GetConsumerValSet(ctx, consumerId)
	require.NoError(t, err)
	require.Empty(t, valSet, "consumer valset must be deleted")

	_, err = k.GetConsumerRemovalTime(ctx, consumerId)
	require.Error(t, err, "removal time must be deleted")
	require.True(t, k.GetConsumerLastAckTime(ctx, consumerId).IsZero(), "last-ack time must be deleted")
	require.Zero(t, k.GetConsumerHighestSentVscId(ctx, consumerId))
	require.Zero(t, k.GetConsumerHighestAckedVscId(ctx, consumerId))
	hasDebt, err := k.ConsumerDebt.Has(ctx, consumerId)
	require.NoError(t, err)
	require.False(t, hasDebt, "debt flag must be deleted")

	has, err := k.ConsumerFeesPerBlockOverride.Has(ctx, consumerId)
	require.NoError(t, err)
	require.False(t, has, "fees-per-block override must be deleted")

	// Nothing is left in any fee-pool share collection, so no balance can be
	// stranded behind a claim nobody can exercise.
	sharesIter, err := k.ConsumerFeePoolShares.Iterate(ctx,
		collections.NewPrefixedTripleRange[uint64, string, sdk.AccAddress](consumerId))
	require.NoError(t, err)
	defer sharesIter.Close()
	require.False(t, sharesIter.Valid(), "fee-pool shares must be cleared")

	totalsIter, err := k.ConsumerFeePoolTotalShares.Iterate(ctx,
		collections.NewPrefixedPairRange[uint64, string](consumerId))
	require.NoError(t, err)
	defer totalsIter.Close()
	require.False(t, totalsIter.Valid(), "fee-pool total shares must be cleared")

	// The tombstone the teardown keeps on purpose.
	owner, err := k.GetConsumerOwnerAddress(ctx, consumerId)
	require.NoError(t, err)
	require.NotEmpty(t, owner, "owner must be kept for explorers")
	_, err = k.GetConsumerMetadata(ctx, consumerId)
	require.NoError(t, err, "metadata must be kept for explorers")
}

// TestRemoveConsumerOwnerErasesPrelaunchedConsumer verifies the pre-launch arm
// of MsgRemoveConsumer: the owner of a consumer that has not launched can
// remove it from either pre-launch phase, the teardown is immediate and leaves
// nothing behind, and the chain id it held can be registered again afterwards.
func TestRemoveConsumerOwnerErasesPrelaunchedConsumer(t *testing.T) {
	owner := sdk.AccAddress([]byte("consumer-owner-addr1")).String()

	testCases := []struct {
		name      string
		spawnTime time.Time
		phase     providertypes.ConsumerPhase
	}{
		{
			name:      "registered",
			spawnTime: time.Time{},
			phase:     providertypes.CONSUMER_PHASE_REGISTERED,
		},
		{
			name:      "initialized",
			spawnTime: time.Unix(2_000_000_000, 0).UTC(),
			phase:     providertypes.CONSUMER_PHASE_INITIALIZED,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
			defer ctrl.Finish()

			mocks.MockStakingKeeper.EXPECT().UnbondingTime(gomock.Any()).
				Return(21*24*time.Hour, nil).AnyTimes()

			const chainId = "retire-1"
			consumerId := createRetirableConsumer(t, k, ctx, owner, chainId, tc.spawnTime)
			require.Equal(t, tc.phase, k.GetConsumerPhase(ctx, consumerId))

			// A validator may assign a consumer key before launch, so make sure
			// the retirement has some to clear.
			providerAddr := providertypes.NewProviderConsAddress([]byte("provider-cons-addr-1"))
			consumerAddr := providertypes.NewConsumerConsAddress([]byte("consumer-cons-addr-1"))
			k.SetValidatorConsumerPubKey(ctx, consumerId, providerAddr,
				cryptotestutil.NewCryptoIdentityFromIntSeed(7).TMProtoCryptoPublicKey())
			k.SetValidatorByConsumerAddr(ctx, consumerId, consumerAddr, providerAddr)

			// Empty fee pool: the teardown sweeps it and finds nothing to move.
			mocks.MockBankKeeper.EXPECT().
				GetAllBalances(ctx, k.GetConsumerFeePoolAddress(consumerId)).
				Return(sdk.NewCoins())

			ms := providerkeeper.NewMsgServerImpl(&k)
			_, err := ms.RemoveConsumer(ctx, &providertypes.MsgRemoveConsumer{
				Signer: owner, ConsumerId: consumerId,
			})
			require.NoError(t, err)

			requireConsumerTornDown(t, k, ctx, consumerId)

			// An initialized consumer was queued for launch: the entry is gone,
			// so BeginBlockLaunchConsumers can never see the retired consumer.
			if tc.phase == providertypes.CONSUMER_PHASE_INITIALIZED {
				queued, err := k.GetConsumersToBeLaunched(ctx, tc.spawnTime)
				require.NoError(t, err)
				require.Empty(t, queued.Ids, "launch queue entry must be dropped")
			}

			// The chain id is registrable again by anyone, including a different owner.
			inUse, err := k.ChainIdInUse(ctx, chainId)
			require.NoError(t, err)
			require.False(t, inUse)

			otherOwner := sdk.AccAddress([]byte("consumer-owner-addr2")).String()
			newConsumerId := createRetirableConsumer(t, k, ctx, otherOwner, chainId, time.Time{})
			require.NotEqual(t, consumerId, newConsumerId)
			gotChainId, err := k.GetConsumerChainId(ctx, newConsumerId)
			require.NoError(t, err)
			require.Equal(t, chainId, gotChainId)
		})
	}
}

// TestRemoveConsumerGovernanceErasesPrelaunchedWhenOwnerKeyIsLost verifies the
// gov side of the pre-launch arm: a consumer whose owner can no longer sign is
// still removable, so neither its state nor its chain id is pinned forever.
func TestRemoveConsumerGovernanceErasesPrelaunchedWhenOwnerKeyIsLost(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	mocks.MockStakingKeeper.EXPECT().UnbondingTime(gomock.Any()).
		Return(21*24*time.Hour, nil).AnyTimes()

	lostKeyOwner := sdk.AccAddress([]byte("lost-key-owner-addr1")).String()
	const chainId = "stranded-1"
	consumerId := createRetirableConsumer(t, k, ctx, lostKeyOwner, chainId, time.Time{})

	mocks.MockBankKeeper.EXPECT().
		GetAllBalances(ctx, k.GetConsumerFeePoolAddress(consumerId)).
		Return(sdk.NewCoins())

	ms := providerkeeper.NewMsgServerImpl(&k)
	_, err := ms.RemoveConsumer(ctx, &providertypes.MsgRemoveConsumer{
		Signer: k.GetAuthority(), ConsumerId: consumerId,
	})
	require.NoError(t, err)

	requireConsumerTornDown(t, k, ctx, consumerId)

	inUse, err := k.ChainIdInUse(ctx, chainId)
	require.NoError(t, err)
	require.False(t, inUse)
}

// TestRemoveConsumerRejectsUnauthorizedSigner verifies that a signer who is
// neither the owner nor the gov authority cannot remove a pre-launch consumer,
// so removal is not a way to destroy someone else's registration.
func TestRemoveConsumerRejectsUnauthorizedSigner(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	mocks.MockStakingKeeper.EXPECT().UnbondingTime(gomock.Any()).
		Return(21*24*time.Hour, nil).AnyTimes()

	owner := sdk.AccAddress([]byte("consumer-owner-addr1")).String()
	stranger := sdk.AccAddress([]byte("some-other-account11")).String()
	const chainId = "guarded-1"
	consumerId := createRetirableConsumer(t, k, ctx, owner, chainId, time.Time{})

	ms := providerkeeper.NewMsgServerImpl(&k)
	_, err := ms.RemoveConsumer(ctx, &providertypes.MsgRemoveConsumer{
		Signer: stranger, ConsumerId: consumerId,
	})
	require.ErrorIs(t, err, providertypes.ErrUnauthorized)

	// Nothing moved: the consumer is intact and still holds its chain id.
	require.Equal(t, providertypes.CONSUMER_PHASE_REGISTERED, k.GetConsumerPhase(ctx, consumerId))
	inUse, err := k.ChainIdInUse(ctx, chainId)
	require.NoError(t, err)
	require.True(t, inUse)
}

// TestRemoveConsumerRejectsUnknownConsumer verifies that a consumer id that was
// never registered is reported as unknown, including for the gov authority.
func TestRemoveConsumerRejectsUnknownConsumer(t *testing.T) {
	k, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	ms := providerkeeper.NewMsgServerImpl(&k)
	for _, signer := range []string{sdk.AccAddress([]byte("consumer-owner-addr1")).String(), k.GetAuthority()} {
		_, err := ms.RemoveConsumer(ctx, &providertypes.MsgRemoveConsumer{
			Signer: signer, ConsumerId: 42,
		})
		require.ErrorIs(t, err, providertypes.ErrUnknownConsumerId)
	}
}

// TestRemoveConsumerPastLaunchIsGovOnlyAndDeferred verifies the
// launched-and-later arms of MsgRemoveConsumer: a launched or paused consumer
// is removable only by the governance authority, and is stopped with erasure
// deferred by the unbonding period rather than erased immediately (its chain
// id stays reserved). A stopped or deleted consumer is rejected for any
// signer.
func TestRemoveConsumerPastLaunchIsGovOnlyAndDeferred(t *testing.T) {
	owner := sdk.AccAddress([]byte("consumer-owner-addr1")).String()

	for _, phase := range []providertypes.ConsumerPhase{
		providertypes.CONSUMER_PHASE_LAUNCHED,
		providertypes.CONSUMER_PHASE_PAUSED,
	} {
		t.Run(phase.String(), func(t *testing.T) {
			k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
			defer ctrl.Finish()

			mocks.MockStakingKeeper.EXPECT().UnbondingTime(gomock.Any()).
				Return(21*24*time.Hour, nil).AnyTimes()

			const chainId = "live-1"
			consumerId := createRetirableConsumer(t, k, ctx, owner, chainId, time.Time{})
			k.SetConsumerPhase(ctx, consumerId, phase)

			ms := providerkeeper.NewMsgServerImpl(&k)

			// Past launch the owner may not remove: real validators are
			// running the chain, so ending it is governance's call.
			_, err := ms.RemoveConsumer(ctx, &providertypes.MsgRemoveConsumer{
				Signer: owner, ConsumerId: consumerId,
			})
			require.Error(t, err)
			require.Contains(t, err.Error(), "invalid authority")
			require.Equal(t, phase, k.GetConsumerPhase(ctx, consumerId), "a rejected removal must not move the phase")

			// Governance removes it: stopped now, erased after unbonding, the
			// chain id stays reserved until the deferred deletion.
			_, err = ms.RemoveConsumer(ctx, &providertypes.MsgRemoveConsumer{
				Signer: k.GetAuthority(), ConsumerId: consumerId,
			})
			require.NoError(t, err)
			require.Equal(t, providertypes.CONSUMER_PHASE_STOPPED, k.GetConsumerPhase(ctx, consumerId))
			inUse, err := k.ChainIdInUse(ctx, chainId)
			require.NoError(t, err)
			require.True(t, inUse)
		})
	}

	terminalCases := []struct {
		phase providertypes.ConsumerPhase
		// A consumer past launch that has not been erased yet still holds its
		// chain id. The deleted case below is set up by phase alone, so its
		// chain id proves nothing here; the real release is covered by
		// TestDeleteConsumerChainReleasesChainIdOnStoppedPath.
		stillHoldsChainId bool
	}{
		{providertypes.CONSUMER_PHASE_STOPPED, true},
		{providertypes.CONSUMER_PHASE_DELETED, false},
	}
	for _, tc := range terminalCases {
		phase := tc.phase
		t.Run(phase.String(), func(t *testing.T) {
			k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
			defer ctrl.Finish()

			mocks.MockStakingKeeper.EXPECT().UnbondingTime(gomock.Any()).
				Return(21*24*time.Hour, nil).AnyTimes()

			const chainId = "live-1"
			consumerId := createRetirableConsumer(t, k, ctx, owner, chainId, time.Time{})
			k.SetConsumerPhase(ctx, consumerId, phase)

			ms := providerkeeper.NewMsgServerImpl(&k)

			// Both arms are rejected on the phase, not on the signer.
			for _, signer := range []string{owner, k.GetAuthority()} {
				_, err := ms.RemoveConsumer(ctx, &providertypes.MsgRemoveConsumer{
					Signer: signer, ConsumerId: consumerId,
				})
				require.ErrorIs(t, err, providertypes.ErrInvalidPhase)
			}

			require.Equal(t, phase, k.GetConsumerPhase(ctx, consumerId))

			if tc.stillHoldsChainId {
				// The chain id is not released while the consumer still exists
				// in a non-terminal phase.
				inUse, err := k.ChainIdInUse(ctx, chainId)
				require.NoError(t, err)
				require.True(t, inUse)
			}
		})
	}
}

// TestRemoveConsumerReturnsFundedFeePool verifies that removing a pre-launch consumer
// whose fee pool holds a deposit pays the depositors back rather than leaving
// the balance behind an address nobody can withdraw from any more.
func TestRemoveConsumerReturnsFundedFeePool(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	mocks.MockStakingKeeper.EXPECT().UnbondingTime(gomock.Any()).
		Return(21*24*time.Hour, nil).AnyTimes()

	owner := sdk.AccAddress([]byte("consumer-owner-addr1")).String()
	depositor := sdk.AccAddress([]byte("fee-pool-depositor11"))
	const chainId = "funded-1"
	consumerId := createRetirableConsumer(t, k, ctx, owner, chainId, time.Time{})
	poolAddr := k.GetConsumerFeePoolAddress(consumerId)

	// A pre-launch deposit: shares for the depositor against a pool balance.
	require.NoError(t, k.ConsumerFeePoolShares.Set(ctx,
		collections.Join3(consumerId, "uphoton", depositor), math.NewInt(100)))
	require.NoError(t, k.ConsumerFeePoolTotalShares.Set(ctx,
		collections.Join(consumerId, "uphoton"), math.NewInt(100)))

	deposit := sdk.NewInt64Coin("uphoton", 500)
	mocks.MockBankKeeper.EXPECT().GetAllBalances(ctx, poolAddr).
		Return(sdk.NewCoins(deposit))
	mocks.MockBankKeeper.EXPECT().GetBalance(ctx, poolAddr, "uphoton").Return(deposit)
	mocks.MockBankKeeper.EXPECT().SendCoinsFromAccountToModule(
		ctx, poolAddr, providertypes.ModuleName, sdk.NewCoins(deposit)).Return(nil)
	mocks.MockBankKeeper.EXPECT().SendCoinsFromModuleToAccount(
		ctx, providertypes.ModuleName, depositor, sdk.NewCoins(deposit)).Return(nil)

	ms := providerkeeper.NewMsgServerImpl(&k)
	_, err := ms.RemoveConsumer(ctx, &providertypes.MsgRemoveConsumer{
		Signer: owner, ConsumerId: consumerId,
	})
	require.NoError(t, err)

	requireConsumerTornDown(t, k, ctx, consumerId)
}

// TestDeleteConsumerChainReleasesChainIdOnStoppedPath verifies the other route
// into deletion: a consumer stopped by the liveness sweep (or by governance)
// keeps its chain id reserved while it is stopped, and only gives it up once
// BeginBlockRemoveConsumers erases it after the unbonding period. Without that
// release a swept consumer could never re-register under its own chain id.
func TestDeleteConsumerChainReleasesChainIdOnStoppedPath(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	mocks.MockStakingKeeper.EXPECT().UnbondingTime(gomock.Any()).
		Return(21*24*time.Hour, nil).AnyTimes()

	owner := sdk.AccAddress([]byte("consumer-owner-addr1")).String()
	const chainId = "swept-1"
	consumerId := createRetirableConsumer(t, k, ctx, owner, chainId, time.Time{})
	k.SetConsumerPhase(ctx, consumerId, providertypes.CONSUMER_PHASE_LAUNCHED)
	k.SetConsumerClientId(ctx, consumerId, "07-tendermint-0")

	// Seed the state a consumer accumulates once it is live, so the teardown
	// assertions below are checking something that was actually there.
	tmPubKey := cryptotestutil.NewCryptoIdentityFromIntSeed(11).TMProtoCryptoPublicKey()
	require.NoError(t, k.SetConsumerValSet(ctx, consumerId,
		[]providertypes.ConsensusValidator{{PublicKey: &tmPubKey, Power: 10}}))
	require.NoError(t, k.SetConsumerGenesis(ctx, consumerId, *vaastypes.DefaultConsumerGenesisState()))
	k.SetInitChainHeight(ctx, consumerId, 100)
	k.SetEquivocationEvidenceMinHeight(ctx, consumerId, 1)
	k.AppendPendingVSCPackets(ctx, consumerId,
		vaastypes.ValidatorSetChangePacketData{ValsetUpdateId: 3})
	require.NoError(t, k.SetConsumerLastAckTime(ctx, consumerId, ctx.BlockTime()))
	k.SetConsumerHighestSentVscId(ctx, consumerId, 5)
	k.SetConsumerHighestAckedVscId(ctx, consumerId, 3)
	k.SetConsumerInDebt(ctx, consumerId, true)
	require.NoError(t, k.ConsumerFeesPerBlockOverride.Set(ctx, consumerId, math.NewInt(9_999)))
	providerAddr := providertypes.NewProviderConsAddress([]byte("provider-cons-addr-2"))
	k.SetValidatorConsumerPubKey(ctx, consumerId, providerAddr, tmPubKey)

	// Stopped: the chain may still be producing blocks and its validators are
	// still slashable, so the chain id stays reserved.
	require.NoError(t, k.StopAndPrepareForConsumerRemoval(ctx, consumerId))
	inUse, err := k.ChainIdInUse(ctx, chainId)
	require.NoError(t, err)
	require.True(t, inUse, "chain id must stay reserved while the consumer is stopped")

	mocks.MockBankKeeper.EXPECT().
		GetAllBalances(ctx, k.GetConsumerFeePoolAddress(consumerId)).
		Return(sdk.NewCoins())

	require.NoError(t, k.DeleteConsumerChain(ctx, consumerId))

	requireConsumerTornDown(t, k, ctx, consumerId)

	inUse, err = k.ChainIdInUse(ctx, chainId)
	require.NoError(t, err)
	require.False(t, inUse, "chain id must be released once the consumer is erased")

	newConsumerId := createRetirableConsumer(t, k, ctx, owner, chainId, time.Time{})
	require.NotEqual(t, consumerId, newConsumerId)
}
