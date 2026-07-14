package keeper_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	testcrypto "github.com/allinbits/vaas/testutil/crypto"
	testkeeper "github.com/allinbits/vaas/testutil/keeper"
	keeper "github.com/allinbits/vaas/x/vaas/provider/keeper"
	providertypes "github.com/allinbits/vaas/x/vaas/provider/types"
)

// makeConsensusValidator is a test helper that builds a ConsensusValidator
// from an ed25519 private key and a power value.
func makeConsensusValidator(t *testing.T, power int64) providertypes.ConsensusValidator {
	t.Helper()
	pk := ed25519.GenPrivKey().PubKey()
	tmPk, err := cryptocodec.ToCmtProtoPublicKey(pk)
	require.NoError(t, err)
	return providertypes.ConsensusValidator{PublicKey: &tmPk, Power: power}
}

func TestFullValSetUpdatesReturnsCompleteSet(t *testing.T) {
	pk1 := ed25519.GenPrivKey().PubKey()
	tmPk1, err := cryptocodec.ToCmtProtoPublicKey(pk1)
	require.NoError(t, err)
	vals := []providertypes.ConsensusValidator{{PublicKey: &tmPk1, Power: 10}}

	updates := keeper.FullValSetUpdates(vals)
	require.Len(t, updates, 1)
	require.Equal(t, int64(10), updates[0].Power)
}

func TestQueueVSCPacketsSnapshotWhenBehind(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	cid := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, cid, providertypes.CONSUMER_PHASE_LAUNCHED)
	k.SetConsumerHighestSentVscId(ctx, cid, 5)
	k.SetConsumerHighestAckedVscId(ctx, cid, 3) // behind: acked < sent: acked < sent

	mocks.MockStakingKeeper.EXPECT().MaxValidators(gomock.Any()).Return(uint32(100), nil).AnyTimes()
	mocks.MockStakingKeeper.EXPECT().GetBondedValidatorsByPower(gomock.Any()).Return([]stakingtypes.Validator{}, nil).AnyTimes()

	require.NoError(t, k.QueueVSCPackets(ctx))
	pending := k.GetPendingVSCPackets(ctx, cid)
	require.Len(t, pending, 1)
	require.True(t, pending[0].IsSnapshot)
}

func TestQueueVSCPacketsDiffWhenCaughtUp(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	cid := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, cid, providertypes.CONSUMER_PHASE_LAUNCHED)
	// acked == sent == 0: caught up
	k.SetConsumerHighestSentVscId(ctx, cid, 0)
	k.SetConsumerHighestAckedVscId(ctx, cid, 0)

	mocks.MockStakingKeeper.EXPECT().MaxValidators(gomock.Any()).Return(uint32(100), nil).AnyTimes()
	mocks.MockStakingKeeper.EXPECT().GetBondedValidatorsByPower(gomock.Any()).Return([]stakingtypes.Validator{}, nil).AnyTimes()

	require.NoError(t, k.QueueVSCPackets(ctx))
	pending := k.GetPendingVSCPackets(ctx, cid)
	require.Len(t, pending, 1)
	require.False(t, pending[0].IsSnapshot)
}

// TestSnapshotAfterLostDiff is the core resync SAFETY property.
//
// When a consumer is BEHIND (acked < sent) the provider must send a full
// snapshot so the consumer can replace its entire set — no reliance on a
// diff that may never have arrived. This test verifies:
//   - the queued packet is flagged as a snapshot (IsSnapshot == true),
//   - the snapshot contains validator A with non-zero power (the survivor
//     is present in the live set and kept in the snapshot), and
//   - validator B is NOT present in the snapshot — proving the snapshot is
//     computed from the live bonded set and not the stale consumer valset.
func TestSnapshotAfterLostDiff(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	// identityA is the surviving validator; identityB has departed.
	identityA := testcrypto.NewCryptoIdentityFromIntSeed(0)
	identityB := testcrypto.NewCryptoIdentityFromIntSeed(1)

	// Build the ConsensusValidator representations used to seed the consumer valset.
	tmPkA := identityA.TMProtoCryptoPublicKey()
	tmPkB := identityB.TMProtoCryptoPublicKey()
	valAConsensus := providertypes.ConsensusValidator{PublicKey: &tmPkA, Power: 10}
	valBConsensus := providertypes.ConsensusValidator{PublicKey: &tmPkB, Power: 5}

	cid := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, cid, providertypes.CONSUMER_PHASE_LAUNCHED)

	// The consumer last knew about {A, B}.
	require.NoError(t, k.SetConsumerValSet(ctx, cid, []providertypes.ConsensusValidator{valAConsensus, valBConsensus}))

	// The consumer is behind: some sent packets have not been acknowledged.
	k.SetConsumerHighestSentVscId(ctx, cid, 5)
	k.SetConsumerHighestAckedVscId(ctx, cid, 3)

	// Build a real staking validator for A to return from the bonded set.
	// B is absent — it has departed from the bonded set.
	stakingValA := identityA.SDKStakingValidator()
	const powerA = int64(10)

	// GetLastBondedValidators calls MaxValidators then GetBondedValidatorsByPower.
	// CreateConsumerValidator calls GetLastValidatorPower for each bonded validator.
	mocks.MockStakingKeeper.EXPECT().MaxValidators(gomock.Any()).Return(uint32(100), nil).AnyTimes()
	mocks.MockStakingKeeper.EXPECT().GetBondedValidatorsByPower(gomock.Any()).Return([]stakingtypes.Validator{stakingValA}, nil).AnyTimes()
	mocks.MockStakingKeeper.EXPECT().GetLastValidatorPower(gomock.Any(), identityA.SDKValOpAddress()).Return(powerA, nil).AnyTimes()

	require.NoError(t, k.QueueVSCPackets(ctx))

	pending := k.GetPendingVSCPackets(ctx, cid)
	require.Len(t, pending, 1)

	pkt := pending[0]
	require.True(t, pkt.IsSnapshot, "expected IsSnapshot=true when consumer is behind")

	// Validator A must appear in the snapshot with non-zero power.
	aKeyStr := tmPkA.String()
	foundA := false
	for _, u := range pkt.ValidatorUpdates {
		if u.PubKey.String() == aKeyStr {
			require.Greater(t, u.Power, int64(0), "validator A must have non-zero power in the snapshot")
			foundA = true
		}
	}
	require.True(t, foundA, "surviving validator A must be present in the snapshot")

	// Validator B was in the stale consumer valset but departed from the bonded
	// set, so it must NOT appear in the snapshot.
	bKeyStr := tmPkB.String()
	for _, u := range pkt.ValidatorUpdates {
		require.NotEqual(t, bKeyStr, u.PubKey.String(),
			"departed validator B must not appear in the snapshot")
	}
}

// TestDiffValidators_RemoveDeparted verifies that a validator present in the
// current set but absent from the next set generates exactly one power-0
// update for that validator.
func TestDiffValidators_RemoveDeparted(t *testing.T) {
	valA := makeConsensusValidator(t, 10)
	valB := makeConsensusValidator(t, 5)

	current := []providertypes.ConsensusValidator{valA, valB}
	next := []providertypes.ConsensusValidator{valA}

	updates := keeper.DiffValidators(current, next)
	require.Len(t, updates, 1, "expected exactly one update (removal of B)")
	require.Equal(t, valB.PublicKey.String(), updates[0].PubKey.String())
	require.Equal(t, int64(0), updates[0].Power, "departed validator must have power=0")
}

// TestDiffValidators_EmptyNextSet verifies that when the next set is empty
// all current validators are removed (power-0 updates), with no additions.
func TestDiffValidators_EmptyNextSet(t *testing.T) {
	valA := makeConsensusValidator(t, 10)
	valB := makeConsensusValidator(t, 5)

	current := []providertypes.ConsensusValidator{valA, valB}
	updates := keeper.DiffValidators(current, nil)

	require.Len(t, updates, 2, "expected two removals")
	for _, u := range updates {
		require.Equal(t, int64(0), u.Power, "all removals must have power=0")
	}
}

// TestFullValSetUpdates_EmptyInput verifies that FullValSetUpdates on a nil
// or empty slice returns an empty (non-nil, len 0) result.
func TestFullValSetUpdates_EmptyInput(t *testing.T) {
	require.Empty(t, keeper.FullValSetUpdates(nil))
	require.Empty(t, keeper.FullValSetUpdates([]providertypes.ConsensusValidator{}))
}
