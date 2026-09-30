package keeper_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"

	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	tmtypes "github.com/cometbft/cometbft/types"

	cryptotestutil "github.com/allinbits/vaas/testutil/crypto"
	testkeeper "github.com/allinbits/vaas/testutil/keeper"
	providerkeeper "github.com/allinbits/vaas/x/vaas/provider/keeper"
	"github.com/allinbits/vaas/x/vaas/provider/types"
)

// punishmentCounters records what the mocked x/staking was asked to do, so a
// test can state how many times a validator was slashed and jailed.
type punishmentCounters struct {
	slashes, jails int
}

// doubleVoteFixture is a launched consumer with one validator that signs
// with signer, wired so HandleConsumerDoubleVoting can punish it: the
// validator lookup, the slash and the jail on x/staking, and a signing-info
// store answering x/slashing the way the real module does, so a tombstone
// applied by one submission is seen by the next.
type doubleVoteFixture struct {
	k        providerkeeper.Keeper
	ctx      sdk.Context
	cid      uint64
	signer   tmtypes.PrivValidator
	pubKey   cryptotypes.PubKey
	consAddr sdk.ConsAddress
	counters *punishmentCounters
	signing  *rotationSigningInfo
}

func newDoubleVoteFixture(t *testing.T, tombstone bool) (doubleVoteFixture, *gomock.Controller) {
	t.Helper()
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))

	blockTime := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	ctx = ctx.WithBlockTime(blockTime)
	params := types.DefaultInfractionParameters()
	params.DoubleSign.Tombstone = tombstone
	k.SetInfractionParams(ctx, params)
	cid, _ := setupRotationConsumer(t, k, ctx, blockTime.Add(-30*24*time.Hour))

	signer := tmtypes.NewMockPV()
	cmtPubKey, err := signer.GetPubKey()
	require.NoError(t, err)
	sdkPubKey, err := cryptocodec.FromCmtPubKeyInterface(cmtPubKey)
	require.NoError(t, err)
	consAddr := sdk.ConsAddress(cmtPubKey.Address())
	val := stakingValidatorFor(t, sdk.ValAddress(sdkPubKey.Address()), sdkPubKey)

	counters := &punishmentCounters{}
	signing := newRotationSigningInfo(consAddr)
	signing.wire(mocks)
	mocks.MockStakingKeeper.EXPECT().GetValidatorByConsAddr(gomock.Any(), consAddr).Return(val, nil).AnyTimes()
	mocks.MockStakingKeeper.EXPECT().GetUnbondingDelegationsFromValidator(gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
	mocks.MockStakingKeeper.EXPECT().GetRedelegationsFromSrcValidator(gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
	mocks.MockStakingKeeper.EXPECT().GetLastValidatorPower(gomock.Any(), gomock.Any()).Return(int64(1000), nil).AnyTimes()
	mocks.MockStakingKeeper.EXPECT().PowerReduction(gomock.Any()).Return(math.NewInt(1)).AnyTimes()
	mocks.MockStakingKeeper.EXPECT().SlashWithInfractionReason(
		gomock.Any(), consAddr, gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
	).DoAndReturn(func(_ context.Context, _ sdk.ConsAddress, _, power int64, fraction math.LegacyDec, _ stakingtypes.Infraction) (math.Int, error) {
		counters.slashes++
		return math.LegacyNewDec(power).Mul(fraction).TruncateInt(), nil
	}).AnyTimes()
	mocks.MockStakingKeeper.EXPECT().Jail(gomock.Any(), consAddr).
		DoAndReturn(func(context.Context, sdk.ConsAddress) error {
			counters.jails++
			return nil
		}).AnyTimes()

	return doubleVoteFixture{k: k, ctx: ctx, cid: cid, signer: signer, pubKey: sdkPubKey, consAddr: consAddr, counters: counters, signing: signing}, ctrl
}

func (f doubleVoteFixture) punished(t *testing.T, height int64) bool {
	t.Helper()
	has, err := f.k.PunishedEquivocations.Has(f.ctx, collections.Join3(f.cid, f.consAddr.Bytes(), height))
	require.NoError(t, err)
	return has
}

// TestDoubleVoteIsPunishedOncePerInfractionWithoutTombstoning: with
// tombstoning off nothing but the record identifies an infraction already
// punished, so the same evidence submitted again is a no-op, while a
// double-sign at another height is a new infraction and is punished.
func TestDoubleVoteIsPunishedOncePerInfractionWithoutTombstoning(t *testing.T) {
	f, ctrl := newDoubleVoteFixture(t, false)
	defer ctrl.Finish()

	evidence := doubleVoteBy(t, f.signer, 55, f.ctx.BlockTime())
	require.NoError(t, f.k.HandleConsumerDoubleVoting(f.ctx, f.cid, evidence, f.pubKey))
	require.Equal(t, 1, f.counters.slashes)
	require.True(t, f.punished(t, 55), "the punished infraction must be recorded")
	require.False(t, f.signing.at(f.consAddr).Tombstoned)

	require.NoError(t, f.k.HandleConsumerDoubleVoting(f.ctx, f.cid, evidence, f.pubKey))
	require.Equal(t, 1, f.counters.slashes, "the same infraction must not be slashed twice")
	require.Equal(t, 1, f.counters.jails)

	require.NoError(t, f.k.HandleConsumerDoubleVoting(f.ctx, f.cid, doubleVoteBy(t, f.signer, 56, f.ctx.BlockTime()), f.pubKey))
	require.Equal(t, 2, f.counters.slashes, "a double-sign at another height is another infraction")
	require.True(t, f.punished(t, 56))
}

// TestDoubleVoteKeepsNoRecordWhenTombstoning: with tombstoning on, the
// tombstone is what stops a second punishment, so no record is written and
// a re-submission is a no-op through the tombstone alone.
func TestDoubleVoteKeepsNoRecordWhenTombstoning(t *testing.T) {
	f, ctrl := newDoubleVoteFixture(t, true)
	defer ctrl.Finish()

	evidence := doubleVoteBy(t, f.signer, 55, f.ctx.BlockTime())
	require.NoError(t, f.k.HandleConsumerDoubleVoting(f.ctx, f.cid, evidence, f.pubKey))
	require.Equal(t, 1, f.counters.slashes)
	require.True(t, f.signing.at(f.consAddr).Tombstoned)
	require.False(t, f.punished(t, 55), "a tombstoning punishment needs no record")

	require.NoError(t, f.k.HandleConsumerDoubleVoting(f.ctx, f.cid, evidence, f.pubKey))
	require.Equal(t, 1, f.counters.slashes)
}

// TestTombstoningForgetsThePunishedEquivocations: records left by an
// earlier, non-tombstoning policy become redundant the moment the validator
// is tombstoned, and are dropped with the punishment that tombstones it.
func TestTombstoningForgetsThePunishedEquivocations(t *testing.T) {
	f, ctrl := newDoubleVoteFixture(t, true)
	defer ctrl.Finish()

	for _, height := range []int64{10, 20} {
		require.NoError(t, f.k.PunishedEquivocations.Set(f.ctx, collections.Join3(f.cid, f.consAddr.Bytes(), height)))
	}

	require.NoError(t, f.k.HandleConsumerDoubleVoting(f.ctx, f.cid, doubleVoteBy(t, f.signer, 55, f.ctx.BlockTime()), f.pubKey))
	require.True(t, f.signing.at(f.consAddr).Tombstoned)
	for _, height := range []int64{10, 20, 55} {
		require.False(t, f.punished(t, height), "height %d must be forgotten once the validator is tombstoned", height)
	}
}

// TestPunishedEquivocationsFollowAConsensusKeyRotation: the record is keyed
// by the validator's live provider consensus address, so a rotation moves
// it, like the downtime records, or the same infraction would be punishable
// again under the new address.
func TestPunishedEquivocationsFollowAConsensusKeyRotation(t *testing.T) {
	k, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	cid, _ := setupRotationConsumer(t, k, ctx, ctx.BlockTime())
	oldAddr := cryptotestutil.NewCryptoIdentityFromIntSeed(41).SDKValConsAddress()
	newAddr := cryptotestutil.NewCryptoIdentityFromIntSeed(42).SDKValConsAddress()
	require.NoError(t, k.PunishedEquivocations.Set(ctx, collections.Join3(cid, oldAddr.Bytes(), int64(55))))

	k.MigrateStateOnConsPubKeyRotation(ctx, types.NewProviderConsAddress(oldAddr), types.NewProviderConsAddress(newAddr))

	has, err := k.PunishedEquivocations.Has(ctx, collections.Join3(cid, oldAddr.Bytes(), int64(55)))
	require.NoError(t, err)
	require.False(t, has, "nothing may be left at the rotated-away address")
	has, err = k.PunishedEquivocations.Has(ctx, collections.Join3(cid, newAddr.Bytes(), int64(55)))
	require.NoError(t, err)
	require.True(t, has, "the record must follow the validator to its new address")
}

// TestDeleteConsumerChainClearsPunishedEquivocations: the consumer's records
// go with it, and only its own.
func TestDeleteConsumerChainClearsPunishedEquivocations(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	addr := cryptotestutil.NewCryptoIdentityFromIntSeed(43).SDKValConsAddress().Bytes()
	deleted := k.FetchAndIncrementConsumerId(ctx)
	kept := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, deleted, types.CONSUMER_PHASE_STOPPED)
	k.SetConsumerClientId(ctx, deleted, "07-tendermint-0")
	poolAddr := k.GetConsumerFeePoolAddress(deleted)
	require.NoError(t, k.FeePoolAddressToConsumerId.Set(ctx, poolAddr, deleted))
	mocks.MockBankKeeper.EXPECT().GetAllBalances(ctx, poolAddr).Return(sdk.NewCoins())
	require.NoError(t, k.PunishedEquivocations.Set(ctx, collections.Join3(deleted, addr, int64(55))))
	require.NoError(t, k.PunishedEquivocations.Set(ctx, collections.Join3(kept, addr, int64(55))))

	require.NoError(t, k.DeleteConsumerChain(ctx, deleted))

	has, err := k.PunishedEquivocations.Has(ctx, collections.Join3(deleted, addr, int64(55)))
	require.NoError(t, err)
	require.False(t, has)
	has, err = k.PunishedEquivocations.Has(ctx, collections.Join3(kept, addr, int64(55)))
	require.NoError(t, err)
	require.True(t, has, "another consumer's record must survive")
}
