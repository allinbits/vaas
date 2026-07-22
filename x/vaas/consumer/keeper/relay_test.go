package keeper_test

import (
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"

	abci "github.com/cometbft/cometbft/abci/types"

	ibctmtypes "github.com/cosmos/ibc-go/v10/modules/light-clients/07-tendermint"

	"cosmossdk.io/math"

	testcrypto "github.com/allinbits/vaas/testutil/crypto"
	testkeeper "github.com/allinbits/vaas/testutil/keeper"
	"github.com/allinbits/vaas/x/vaas/types"
)

func TestOnRecvVSCPacketV2(t *testing.T) {
	providerClientID := "07-tendermint-0"

	pk1, err := cryptocodec.ToCmtProtoPublicKey(ed25519.GenPrivKey().PubKey())
	require.NoError(t, err)
	pk2, err := cryptocodec.ToCmtProtoPublicKey(ed25519.GenPrivKey().PubKey())
	require.NoError(t, err)
	pk3, err := cryptocodec.ToCmtProtoPublicKey(ed25519.GenPrivKey().PubKey())
	require.NoError(t, err)

	changes1 := []abci.ValidatorUpdate{
		{PubKey: pk1, Power: 30},
		{PubKey: pk2, Power: 20},
	}

	changes2 := []abci.ValidatorUpdate{
		{PubKey: pk2, Power: 40},
		{PubKey: pk3, Power: 10},
	}

	consumerKeeper, ctx, ctrl, mocks := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()
	testkeeper.StubClientState(mocks, "provider-0")

	pd1 := types.NewValidatorSetChangePacketData(changes1, 1)
	err = consumerKeeper.OnRecvVSCPacketV2(ctx, providerClientID, pd1)
	require.NoError(t, err, "first packet should succeed")

	clientID, found := consumerKeeper.GetProviderClientID(ctx)
	require.True(t, found)
	require.Equal(t, providerClientID, clientID)

	pendingChanges, ok := consumerKeeper.GetPendingChanges(ctx)
	require.True(t, ok)
	require.Equal(t, 2, len(pendingChanges.ValidatorUpdates))

	highestID, _, err := consumerKeeper.GetHighestValsetUpdateID(ctx)
	require.NoError(t, err)
	require.Equal(t, uint64(1), highestID)

	pd2 := types.NewValidatorSetChangePacketData(changes2, 2)
	err = consumerKeeper.OnRecvVSCPacketV2(ctx, providerClientID, pd2)
	require.NoError(t, err, "second packet should succeed")

	highestID, _, err = consumerKeeper.GetHighestValsetUpdateID(ctx)
	require.NoError(t, err)
	require.Equal(t, uint64(2), highestID)

	differentClientID := "07-tendermint-999"
	pd3 := types.NewValidatorSetChangePacketData(changes1, 3)
	err = consumerKeeper.OnRecvVSCPacketV2(ctx, differentClientID, pd3)
	require.NoError(t, err, "packet from different client should succeed (IBC v2 validates counterparties)")

	highestID, _, err = consumerKeeper.GetHighestValsetUpdateID(ctx)
	require.NoError(t, err)
	require.Equal(t, uint64(3), highestID)
}

func TestOnRecvVSCPacketV2OutOfOrder(t *testing.T) {
	providerClientID := "07-tendermint-0"

	pk1, err := cryptocodec.ToCmtProtoPublicKey(ed25519.GenPrivKey().PubKey())
	require.NoError(t, err)
	pk2, err := cryptocodec.ToCmtProtoPublicKey(ed25519.GenPrivKey().PubKey())
	require.NoError(t, err)

	consumerKeeper, ctx, ctrl, mocks := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()
	testkeeper.StubClientState(mocks, "provider-0")

	changes5 := []abci.ValidatorUpdate{{PubKey: pk1, Power: 50}}
	pd5 := types.NewValidatorSetChangePacketData(changes5, 5)
	err = consumerKeeper.OnRecvVSCPacketV2(ctx, providerClientID, pd5)
	require.NoError(t, err)

	highestID, _, err := consumerKeeper.GetHighestValsetUpdateID(ctx)
	require.NoError(t, err)
	require.Equal(t, uint64(5), highestID)

	pendingChanges, _ := consumerKeeper.GetPendingChanges(ctx)
	require.Equal(t, int64(50), pendingChanges.ValidatorUpdates[0].Power)

	changes3 := []abci.ValidatorUpdate{{PubKey: pk2, Power: 30}}
	pd3 := types.NewValidatorSetChangePacketData(changes3, 3)
	err = consumerKeeper.OnRecvVSCPacketV2(ctx, providerClientID, pd3)
	require.NoError(t, err, "out-of-order packet should be acknowledged without error")

	highestID, _, err = consumerKeeper.GetHighestValsetUpdateID(ctx)
	require.NoError(t, err)
	require.Equal(t, uint64(5), highestID)

	pendingChanges, _ = consumerKeeper.GetPendingChanges(ctx)
	require.Equal(t, 1, len(pendingChanges.ValidatorUpdates))
	require.Equal(t, int64(50), pendingChanges.ValidatorUpdates[0].Power)

	changes6 := []abci.ValidatorUpdate{{PubKey: pk2, Power: 60}}
	pd6 := types.NewValidatorSetChangePacketData(changes6, 6)
	err = consumerKeeper.OnRecvVSCPacketV2(ctx, providerClientID, pd6)
	require.NoError(t, err)

	highestID, _, err = consumerKeeper.GetHighestValsetUpdateID(ctx)
	require.NoError(t, err)
	require.Equal(t, uint64(6), highestID)

	pendingChanges, _ = consumerKeeper.GetPendingChanges(ctx)
	require.Equal(t, 2, len(pendingChanges.ValidatorUpdates))
}

func TestOnRecvVSCPacketV2FirstPacketNotDropped(t *testing.T) {
	providerClientID := "07-tendermint-0"

	pk1, err := cryptocodec.ToCmtProtoPublicKey(ed25519.GenPrivKey().PubKey())
	require.NoError(t, err)

	consumerKeeper, ctx, ctrl, mocks := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()
	testkeeper.StubClientState(mocks, "provider-0")

	_, found, err := consumerKeeper.GetHighestValsetUpdateID(ctx)
	require.NoError(t, err)
	require.False(t, found, "unset HighestValsetUpdateID should return found=false")

	changes := []abci.ValidatorUpdate{{PubKey: pk1, Power: 100}}
	pd1 := types.NewValidatorSetChangePacketData(changes, 1)

	err = consumerKeeper.OnRecvVSCPacketV2(ctx, providerClientID, pd1)
	require.NoError(t, err, "first packet should be processed when no highest ID is set")

	pendingChanges, ok := consumerKeeper.GetPendingChanges(ctx)
	require.True(t, ok)
	require.Equal(t, int64(100), pendingChanges.ValidatorUpdates[0].Power)

	highestID, found, err := consumerKeeper.GetHighestValsetUpdateID(ctx)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, uint64(1), highestID)
}

func TestOnRecvVSCPacketV2AccumulatesChanges(t *testing.T) {
	providerClientID := "07-tendermint-0"

	pk1, err := cryptocodec.ToCmtProtoPublicKey(ed25519.GenPrivKey().PubKey())
	require.NoError(t, err)
	pk2, err := cryptocodec.ToCmtProtoPublicKey(ed25519.GenPrivKey().PubKey())
	require.NoError(t, err)
	pk3, err := cryptocodec.ToCmtProtoPublicKey(ed25519.GenPrivKey().PubKey())
	require.NoError(t, err)

	changes1 := []abci.ValidatorUpdate{
		{PubKey: pk1, Power: 30},
		{PubKey: pk2, Power: 20},
	}

	changes2 := []abci.ValidatorUpdate{
		{PubKey: pk2, Power: 40},
		{PubKey: pk3, Power: 10},
	}

	consumerKeeper, ctx, ctrl, mocks := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()
	testkeeper.StubClientState(mocks, "provider-0")

	pd1 := types.NewValidatorSetChangePacketData(changes1, 1)
	err = consumerKeeper.OnRecvVSCPacketV2(ctx, providerClientID, pd1)
	require.NoError(t, err)

	pd2 := types.NewValidatorSetChangePacketData(changes2, 2)
	err = consumerKeeper.OnRecvVSCPacketV2(ctx, providerClientID, pd2)
	require.NoError(t, err)

	pendingChanges, ok := consumerKeeper.GetPendingChanges(ctx)
	require.True(t, ok)

	expected := types.ValidatorSetChangePacketData{ValidatorUpdates: []abci.ValidatorUpdate{
		{PubKey: pk1, Power: 30},
		{PubKey: pk2, Power: 40},
		{PubKey: pk3, Power: 10},
	}}

	sort.SliceStable(pendingChanges.ValidatorUpdates, func(i, j int) bool {
		return pendingChanges.ValidatorUpdates[i].PubKey.Compare(pendingChanges.ValidatorUpdates[j].PubKey) == -1
	})
	sort.SliceStable(expected.ValidatorUpdates, func(i, j int) bool {
		return expected.ValidatorUpdates[i].PubKey.Compare(expected.ValidatorUpdates[j].PubKey) == -1
	})
	require.Equal(t, expected, *pendingChanges)
}

func TestOnRecvVSCPacketV2DuplicateUpdates(t *testing.T) {
	providerClientID := "07-tendermint-0"

	consumerKeeper, ctx, ctrl, mocks := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()
	testkeeper.StubClientState(mocks, "provider-0")

	cId := testcrypto.NewCryptoIdentityFromIntSeed(43278947)
	valUpdates := []abci.ValidatorUpdate{
		{PubKey: cId.TMProtoCryptoPublicKey(), Power: 0},
		{PubKey: cId.TMProtoCryptoPublicKey(), Power: 473289},
	}
	vscData := types.NewValidatorSetChangePacketData(valUpdates, 1)

	err := consumerKeeper.OnRecvVSCPacketV2(ctx, providerClientID, vscData)
	require.NoError(t, err)

	gotPendingChanges, ok := consumerKeeper.GetPendingChanges(ctx)
	require.True(t, ok)

	require.Equal(t, 1, len(gotPendingChanges.ValidatorUpdates))
	require.Equal(t, valUpdates[1], gotPendingChanges.ValidatorUpdates[0])
}

// TestOnRecvVSCPacketV2DebtStatus verifies each received VSC packet
// overwrites the consumer's in-debt flag with the value carried by the
// packet, including on empty-update "heartbeat" packets.
func TestOnRecvVSCPacketV2DebtStatus(t *testing.T) {
	providerClientID := "07-tendermint-0"

	consumerKeeper, ctx, ctrl, mocks := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()
	testkeeper.StubClientState(mocks, "provider-0")

	require.False(t, consumerKeeper.IsConsumerInDebt(ctx))

	pd1 := types.NewValidatorSetChangePacketData(nil, 1)
	pd1.ConsumerInDebt = true
	require.NoError(t, consumerKeeper.OnRecvVSCPacketV2(ctx, providerClientID, pd1))
	require.True(t, consumerKeeper.IsConsumerInDebt(ctx))

	pd2 := types.NewValidatorSetChangePacketData(nil, 2)
	pd2.ConsumerInDebt = false
	require.NoError(t, consumerKeeper.OnRecvVSCPacketV2(ctx, providerClientID, pd2))
	require.False(t, consumerKeeper.IsConsumerInDebt(ctx))
}

func TestConsumerVSCStaleness(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()
	testkeeper.StubClientState(mocks, "provider-0")

	// Use a non-default threshold so this proves IsVSCStale reads the param
	// value (not a constant) and that the boundary tracks the param.
	const threshold = 2 * time.Hour
	k.SetParams(ctx, types.NewConsumerParams(
		true,
		types.DefaultVAASTimeoutPeriod,
		types.DefaultHistoricalEntries,
		types.DefaultConsumerUnbondingPeriod,
		threshold,
	))

	require.False(t, k.IsVSCStale(ctx)) // absent -> BlockTime -> fresh

	k.SetLastVSCRecvTime(ctx, ctx.BlockTime())
	require.False(t, k.IsVSCStale(ctx))

	// Exactly at the threshold is not stale (the check is strict >).
	atBoundary := ctx.WithBlockTime(ctx.BlockTime().Add(threshold))
	require.False(t, k.IsVSCStale(atBoundary))

	// Past the threshold is stale.
	stale := ctx.WithBlockTime(ctx.BlockTime().Add(threshold + time.Minute))
	require.True(t, k.IsVSCStale(stale))
}

func TestSnapshotReplacesValidatorSet(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()
	testkeeper.StubClientState(mocks, "provider-0")

	pkA := ed25519.GenPrivKey().PubKey()
	pkB := ed25519.GenPrivKey().PubKey()
	tmA, err := cryptocodec.ToCmtProtoPublicKey(pkA)
	require.NoError(t, err)
	tmB, err := cryptocodec.ToCmtProtoPublicKey(pkB)
	require.NoError(t, err)

	// Seed current CC set: A=10, B=5.
	k.ApplyCCValidatorChanges(ctx, []abci.ValidatorUpdate{{PubKey: tmA, Power: 10}, {PubKey: tmB, Power: 5}})
	require.NoError(t, k.SetHighestValsetUpdateID(ctx, 1))
	k.SetProviderClientID(ctx, "07-tendermint-0")

	snap := types.NewValidatorSetChangePacketData([]abci.ValidatorUpdate{{PubKey: tmA, Power: 10}}, 2)
	snap.IsSnapshot = true
	require.NoError(t, k.OnRecvVSCPacketV2(ctx, "07-tendermint-0", snap))

	pending, ok := k.GetPendingChanges(ctx)
	require.True(t, ok)
	// snapshot must produce exactly 2 entries: A at 10 and B at 0 (explicit removal)
	require.Len(t, pending.ValidatorUpdates, 2)
	powers := map[string]int64{}
	for _, u := range pending.ValidatorUpdates {
		powers[u.PubKey.String()] = u.Power
	}
	require.Equal(t, int64(10), powers[tmA.String()])
	require.Equal(t, int64(0), powers[tmB.String()]) // B explicitly removed
}

func TestOnRecvVSCRecordsRecvTime(t *testing.T) {
	providerClientID := "07-tendermint-0"

	pk1, err := cryptocodec.ToCmtProtoPublicKey(ed25519.GenPrivKey().PubKey())
	require.NoError(t, err)

	consumerKeeper, ctx, ctrl, mocks := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()
	testkeeper.StubClientState(mocks, "provider-0")

	advancedTime := ctx.BlockTime().Add(10 * time.Minute)
	ctx = ctx.WithBlockTime(advancedTime)

	changes := []abci.ValidatorUpdate{{PubKey: pk1, Power: 100}}
	pd := types.NewValidatorSetChangePacketData(changes, 1)
	err = consumerKeeper.OnRecvVSCPacketV2(ctx, providerClientID, pd)
	require.NoError(t, err)

	got := consumerKeeper.GetLastVSCRecvTime(ctx)
	require.Equal(t, advancedTime, got)
}

// TestDedupDoesNotResetLastVSCRecvTime verifies that an out-of-order (duplicate)
// packet -- one whose ValsetUpdateId <= HighestValsetUpdateID -- returns early
// before recording block time, so a stale replay cannot silently reset the clock.
func TestDedupDoesNotResetLastVSCRecvTime(t *testing.T) {
	providerClientID := "07-tendermint-0"

	pk1, err := cryptocodec.ToCmtProtoPublicKey(ed25519.GenPrivKey().PubKey())
	require.NoError(t, err)

	k, ctx, ctrl, mocks := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()
	testkeeper.StubClientState(mocks, "provider-0")

	// Deliver packet at blockTime T1 -- records lastVSCRecvTime = T1.
	t1 := ctx.BlockTime().Add(5 * time.Minute)
	ctx1 := ctx.WithBlockTime(t1)
	pd1 := types.NewValidatorSetChangePacketData([]abci.ValidatorUpdate{{PubKey: pk1, Power: 10}}, 5)
	require.NoError(t, k.OnRecvVSCPacketV2(ctx1, providerClientID, pd1))
	require.Equal(t, t1, k.GetLastVSCRecvTime(ctx1))

	// Deliver an out-of-order packet (vscId 3 < highest 5) at a later blockTime T2.
	// The dedup early-return fires before SetLastVSCRecvTime, so the clock must NOT advance.
	t2 := t1.Add(10 * time.Minute)
	ctx2 := ctx.WithBlockTime(t2)
	pd2 := types.NewValidatorSetChangePacketData([]abci.ValidatorUpdate{{PubKey: pk1, Power: 99}}, 3)
	require.NoError(t, k.OnRecvVSCPacketV2(ctx2, providerClientID, pd2))

	got := k.GetLastVSCRecvTime(ctx2)
	require.Equal(t, t1, got, "dedup replay must not reset lastVSCRecvTime")
}

// TestRecvPacketAfterStalenessLiftsStale drives the consumer into safe mode by
// making the last VSC receipt time appear far in the past, then delivers a
// higher-id packet at a fresh block time and verifies IsVSCStale returns false.
func TestRecvPacketAfterStalenessLiftsStale(t *testing.T) {
	providerClientID := "07-tendermint-0"

	pk1, err := cryptocodec.ToCmtProtoPublicKey(ed25519.GenPrivKey().PubKey())
	require.NoError(t, err)

	k, ctx, ctrl, mocks := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()
	testkeeper.StubClientState(mocks, "provider-0")

	const threshold = 2 * time.Hour
	k.SetParams(ctx, types.NewConsumerParams(
		true,
		types.DefaultVAASTimeoutPeriod,
		types.DefaultHistoricalEntries,
		types.DefaultConsumerUnbondingPeriod,
		threshold,
	))

	// Seed the consumer with an initial packet delivered at blockTime now.
	baseTime := ctx.BlockTime()
	ctxBase := ctx.WithBlockTime(baseTime)
	pd1 := types.NewValidatorSetChangePacketData([]abci.ValidatorUpdate{{PubKey: pk1, Power: 10}}, 1)
	require.NoError(t, k.OnRecvVSCPacketV2(ctxBase, providerClientID, pd1))

	// Fast-forward block time past the threshold so the consumer is stale.
	staleCtx := ctx.WithBlockTime(baseTime.Add(threshold + time.Minute))
	require.True(t, k.IsVSCStale(staleCtx), "consumer should be stale before resync")

	// Deliver a higher-id packet at the stale block time -- this constitutes resync.
	pd2 := types.NewValidatorSetChangePacketData([]abci.ValidatorUpdate{{PubKey: pk1, Power: 20}}, 2)
	require.NoError(t, k.OnRecvVSCPacketV2(staleCtx, providerClientID, pd2))

	// After the resync, IsVSCStale should be false because lastVSCRecvTime was updated.
	require.False(t, k.IsVSCStale(staleCtx), "resync must lift safe mode")
}

// TestOnRecvVSCPacketStagesDowntimeParams verifies that a VSC packet carrying
// downtime params different from the consumer's current ones is staged (not
// applied live), and that a stale (already-seen) vsc id does not stage
// anything, even if it carries different downtime params.
func TestOnRecvVSCPacketStagesDowntimeParams(t *testing.T) {
	providerClientID := "07-tendermint-0"

	k, ctx, ctrl, mocks := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()
	testkeeper.StubClientState(mocks, "provider-0")

	initialParams := types.DefaultConsumerParams()
	k.SetParams(ctx, initialParams)

	staged := types.DowntimeParams{
		SignedBlocksWindow: initialParams.SignedBlocksWindow * 2,
		MinSignedPerWindow: math.LegacyMustNewDecFromStr("0.75"),
	}

	pd := types.NewValidatorSetChangePacketData(nil, 1)
	pd.DowntimeParams = &staged
	require.NoError(t, k.OnRecvVSCPacketV2(ctx, providerClientID, pd))

	// The staged value is recorded...
	got, err := k.StagedDowntimeParams.Get(ctx)
	require.NoError(t, err)
	require.Equal(t, staged.SignedBlocksWindow, got.SignedBlocksWindow)
	require.True(t, staged.MinSignedPerWindow.Equal(got.MinSignedPerWindow))

	// ...but the live params are untouched until the next window boundary.
	live := k.GetConsumerParams(ctx)
	require.Equal(t, initialParams.SignedBlocksWindow, live.SignedBlocksWindow)
	require.True(t, initialParams.MinSignedPerWindow.Equal(live.MinSignedPerWindow))

	// Clear the staged value, then replay a stale (already-seen) vsc id
	// carrying yet another set of downtime params: it must not be staged.
	require.NoError(t, k.StagedDowntimeParams.Remove(ctx))

	stale := types.DowntimeParams{
		SignedBlocksWindow: initialParams.SignedBlocksWindow * 3,
		MinSignedPerWindow: math.LegacyMustNewDecFromStr("0.9"),
	}
	pdStale := types.NewValidatorSetChangePacketData(nil, 1)
	pdStale.DowntimeParams = &stale
	require.NoError(t, k.OnRecvVSCPacketV2(ctx, providerClientID, pdStale))

	_, err = k.StagedDowntimeParams.Get(ctx)
	require.Error(t, err, "a stale vsc id must not stage downtime params")
}

// TestSnapshotResyncEmitsEvent verifies the consumer emits EventTypeSnapshotResync
// when (and only when) it applies a snapshot packet, so the resync is observable
// (used by the e2e to distinguish a snapshot from a resent diff).
func TestSnapshotResyncEmitsEvent(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()
	testkeeper.StubClientState(mocks, "provider-0")

	pk := ed25519.GenPrivKey().PubKey()
	tm, err := cryptocodec.ToCmtProtoPublicKey(pk)
	require.NoError(t, err)
	k.ApplyCCValidatorChanges(ctx, []abci.ValidatorUpdate{{PubKey: tm, Power: 10}})
	require.NoError(t, k.SetHighestValsetUpdateID(ctx, 1))
	k.SetProviderClientID(ctx, "07-tendermint-0")

	countSnapshotEvents := func() int {
		n := 0
		for _, ev := range ctx.EventManager().Events() {
			if ev.Type == types.EventTypeSnapshotResync {
				n++
			}
		}
		return n
	}

	// An ordinary diff packet must NOT emit the snapshot-resync event.
	diff := types.NewValidatorSetChangePacketData([]abci.ValidatorUpdate{{PubKey: tm, Power: 12}}, 2)
	require.NoError(t, k.OnRecvVSCPacketV2(ctx, "07-tendermint-0", diff))
	require.Zero(t, countSnapshotEvents(), "a diff packet must not emit a snapshot-resync event")

	// A snapshot packet must emit exactly one.
	snap := types.NewValidatorSetChangePacketData([]abci.ValidatorUpdate{{PubKey: tm, Power: 15}}, 3)
	snap.IsSnapshot = true
	require.NoError(t, k.OnRecvVSCPacketV2(ctx, "07-tendermint-0", snap))
	require.Equal(t, 1, countSnapshotEvents(), "a snapshot packet must emit exactly one snapshot-resync event")
}

// TestSnapshotPowerChange seeds the CC set with A=10 and delivers a snapshot
// that changes A's power to 50. PendingChanges must contain A at power 50.
func TestSnapshotPowerChange(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()
	testkeeper.StubClientState(mocks, "provider-0")

	pkA, err := cryptocodec.ToCmtProtoPublicKey(ed25519.GenPrivKey().PubKey())
	require.NoError(t, err)

	k.ApplyCCValidatorChanges(ctx, []abci.ValidatorUpdate{{PubKey: pkA, Power: 10}})
	require.NoError(t, k.SetHighestValsetUpdateID(ctx, 1))
	k.SetProviderClientID(ctx, "07-tendermint-0")

	snap := types.NewValidatorSetChangePacketData([]abci.ValidatorUpdate{{PubKey: pkA, Power: 50}}, 2)
	snap.IsSnapshot = true
	require.NoError(t, k.OnRecvVSCPacketV2(ctx, "07-tendermint-0", snap))

	pending, ok := k.GetPendingChanges(ctx)
	require.True(t, ok)
	require.Len(t, pending.ValidatorUpdates, 1)
	require.Equal(t, int64(50), pending.ValidatorUpdates[0].Power)
}

// TestSnapshotAddsNewValidator seeds {A} and delivers a snapshot {A, B}.
// PendingChanges must contain both A and B at their snapshot powers, with no
// power-0 entry for A.
func TestSnapshotAddsNewValidator(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()
	testkeeper.StubClientState(mocks, "provider-0")

	pkA, err := cryptocodec.ToCmtProtoPublicKey(ed25519.GenPrivKey().PubKey())
	require.NoError(t, err)
	pkB, err := cryptocodec.ToCmtProtoPublicKey(ed25519.GenPrivKey().PubKey())
	require.NoError(t, err)

	k.ApplyCCValidatorChanges(ctx, []abci.ValidatorUpdate{{PubKey: pkA, Power: 10}})
	require.NoError(t, k.SetHighestValsetUpdateID(ctx, 1))
	k.SetProviderClientID(ctx, "07-tendermint-0")

	snap := types.NewValidatorSetChangePacketData([]abci.ValidatorUpdate{
		{PubKey: pkA, Power: 10},
		{PubKey: pkB, Power: 20},
	}, 2)
	snap.IsSnapshot = true
	require.NoError(t, k.OnRecvVSCPacketV2(ctx, "07-tendermint-0", snap))

	pending, ok := k.GetPendingChanges(ctx)
	require.True(t, ok)
	require.Len(t, pending.ValidatorUpdates, 2)

	powers := map[string]int64{}
	for _, u := range pending.ValidatorUpdates {
		powers[u.PubKey.String()] = u.Power
	}
	require.Equal(t, int64(10), powers[pkA.String()])
	require.Equal(t, int64(20), powers[pkB.String()])
}

// TestSnapshotMultipleRemovals seeds {A, B, C} and delivers a snapshot {A only}.
// PendingChanges must contain exactly 3 entries: A at its power, B=0, C=0.
func TestSnapshotMultipleRemovals(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()
	testkeeper.StubClientState(mocks, "provider-0")

	pkA, err := cryptocodec.ToCmtProtoPublicKey(ed25519.GenPrivKey().PubKey())
	require.NoError(t, err)
	pkB, err := cryptocodec.ToCmtProtoPublicKey(ed25519.GenPrivKey().PubKey())
	require.NoError(t, err)
	pkC, err := cryptocodec.ToCmtProtoPublicKey(ed25519.GenPrivKey().PubKey())
	require.NoError(t, err)

	k.ApplyCCValidatorChanges(ctx, []abci.ValidatorUpdate{
		{PubKey: pkA, Power: 30},
		{PubKey: pkB, Power: 20},
		{PubKey: pkC, Power: 10},
	})
	require.NoError(t, k.SetHighestValsetUpdateID(ctx, 1))
	k.SetProviderClientID(ctx, "07-tendermint-0")

	snap := types.NewValidatorSetChangePacketData([]abci.ValidatorUpdate{{PubKey: pkA, Power: 30}}, 2)
	snap.IsSnapshot = true
	require.NoError(t, k.OnRecvVSCPacketV2(ctx, "07-tendermint-0", snap))

	pending, ok := k.GetPendingChanges(ctx)
	require.True(t, ok)
	require.Len(t, pending.ValidatorUpdates, 3)

	powers := map[string]int64{}
	for _, u := range pending.ValidatorUpdates {
		powers[u.PubKey.String()] = u.Power
	}
	require.Equal(t, int64(30), powers[pkA.String()])
	require.Equal(t, int64(0), powers[pkB.String()])
	require.Equal(t, int64(0), powers[pkC.String()])
}

// TestSnapshotEmptyRemovesAll seeds {A, B} and delivers a snapshot with zero
// validator updates (IsSnapshot=true, empty list). PendingChanges must contain
// exactly two entries: A=0 and B=0.
func TestSnapshotEmptyRemovesAll(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()
	testkeeper.StubClientState(mocks, "provider-0")

	pkA, err := cryptocodec.ToCmtProtoPublicKey(ed25519.GenPrivKey().PubKey())
	require.NoError(t, err)
	pkB, err := cryptocodec.ToCmtProtoPublicKey(ed25519.GenPrivKey().PubKey())
	require.NoError(t, err)

	k.ApplyCCValidatorChanges(ctx, []abci.ValidatorUpdate{
		{PubKey: pkA, Power: 10},
		{PubKey: pkB, Power: 5},
	})
	require.NoError(t, k.SetHighestValsetUpdateID(ctx, 1))
	k.SetProviderClientID(ctx, "07-tendermint-0")

	snap := types.NewValidatorSetChangePacketData(nil, 2)
	snap.IsSnapshot = true
	require.NoError(t, k.OnRecvVSCPacketV2(ctx, "07-tendermint-0", snap))

	pending, ok := k.GetPendingChanges(ctx)
	require.True(t, ok)
	require.Len(t, pending.ValidatorUpdates, 2)

	for _, u := range pending.ValidatorUpdates {
		require.Equal(t, int64(0), u.Power, "all validators should be removed by empty snapshot")
	}
}

// TestSnapshotReplacesEarlierPendingChanges delivers a diff packet to populate
// PendingChanges, then immediately delivers a snapshot. The final PendingChanges
// must contain only snapshot-derived updates (snapshot replaces, does not merge).
func TestSnapshotReplacesEarlierPendingChanges(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()
	testkeeper.StubClientState(mocks, "provider-0")

	pkA, err := cryptocodec.ToCmtProtoPublicKey(ed25519.GenPrivKey().PubKey())
	require.NoError(t, err)
	pkB, err := cryptocodec.ToCmtProtoPublicKey(ed25519.GenPrivKey().PubKey())
	require.NoError(t, err)

	k.SetProviderClientID(ctx, "07-tendermint-0")

	// First: a diff packet introducing A and B.
	diff := types.NewValidatorSetChangePacketData([]abci.ValidatorUpdate{
		{PubKey: pkA, Power: 10},
		{PubKey: pkB, Power: 20},
	}, 1)
	require.NoError(t, k.OnRecvVSCPacketV2(ctx, "07-tendermint-0", diff))

	// Apply so the CC set reflects A and B.
	k.ApplyCCValidatorChanges(ctx, []abci.ValidatorUpdate{
		{PubKey: pkA, Power: 10},
		{PubKey: pkB, Power: 20},
	})

	// Second: a snapshot containing only A. B must be removed; the earlier diff
	// entry for B must not survive in PendingChanges.
	snap := types.NewValidatorSetChangePacketData([]abci.ValidatorUpdate{{PubKey: pkA, Power: 10}}, 2)
	snap.IsSnapshot = true
	require.NoError(t, k.OnRecvVSCPacketV2(ctx, "07-tendermint-0", snap))

	pending, ok := k.GetPendingChanges(ctx)
	require.True(t, ok)

	powers := map[string]int64{}
	for _, u := range pending.ValidatorUpdates {
		powers[u.PubKey.String()] = u.Power
	}
	// Snapshot produced: A at 10, B at 0 (explicit removal). No other entries.
	require.Len(t, pending.ValidatorUpdates, 2)
	require.Equal(t, int64(10), powers[pkA.String()])
	require.Equal(t, int64(0), powers[pkB.String()])
}

// TestSnapshotNoDoubleEmitForUnchangedValidator seeds {A=10} and delivers a
// snapshot {A=10} (no power change). PendingChanges must contain exactly one
// entry for A -- the identity match must not duplicate the update.
func TestSnapshotNoDoubleEmitForUnchangedValidator(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()
	testkeeper.StubClientState(mocks, "provider-0")

	pkA, err := cryptocodec.ToCmtProtoPublicKey(ed25519.GenPrivKey().PubKey())
	require.NoError(t, err)

	k.ApplyCCValidatorChanges(ctx, []abci.ValidatorUpdate{{PubKey: pkA, Power: 10}})
	require.NoError(t, k.SetHighestValsetUpdateID(ctx, 1))
	k.SetProviderClientID(ctx, "07-tendermint-0")

	snap := types.NewValidatorSetChangePacketData([]abci.ValidatorUpdate{{PubKey: pkA, Power: 10}}, 2)
	snap.IsSnapshot = true
	require.NoError(t, k.OnRecvVSCPacketV2(ctx, "07-tendermint-0", snap))

	pending, ok := k.GetPendingChanges(ctx)
	require.True(t, ok)
	require.Len(t, pending.ValidatorUpdates, 1, "unchanged validator must appear exactly once in snapshot pending changes")
	require.Equal(t, int64(10), pending.ValidatorUpdates[0].Power)
}

// TestOnRecvVSCPacketV2PinsProviderChainIdOnFirstPacket verifies that the
// first VSC packet ever accepted pins ProviderChainId from the destination
// client's tendermint chain id (see authenticateProviderChainID in relay.go),
// establishing the value later packets get checked against.
func TestOnRecvVSCPacketV2PinsProviderChainIdOnFirstPacket(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	clientID := "07-tendermint-0"
	mocks.MockClientKeeper.EXPECT().GetClientState(gomock.Any(), clientID).
		Return(&ibctmtypes.ClientState{ChainId: "provider-chain"}, true).AnyTimes()

	_, found := k.GetProviderChainId(ctx)
	require.False(t, found, "no chain id should be pinned before the first packet is accepted")

	pk1, err := cryptocodec.ToCmtProtoPublicKey(ed25519.GenPrivKey().PubKey())
	require.NoError(t, err)
	pd := types.NewValidatorSetChangePacketData([]abci.ValidatorUpdate{{PubKey: pk1, Power: 10}}, 1)
	require.NoError(t, k.OnRecvVSCPacketV2(ctx, clientID, pd))

	pinned, found := k.GetProviderChainId(ctx)
	require.True(t, found)
	require.Equal(t, "provider-chain", pinned)
}

// TestOnRecvVSCPacketV2RejectsDifferentChainId verifies that once a provider
// chain id is pinned, a VSC packet delivered over a client tracking a
// different chain id is rejected wholesale: the valset, ProviderClientID and
// LastVSCRecvTime are all left exactly as they were, and nothing is staged.
// Without this check, anyone able to stand up their own IBC v2 client and
// get it routed to the consumer could impersonate the provider.
func TestOnRecvVSCPacketV2RejectsDifferentChainId(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	legitClientID := "07-tendermint-0"
	rogueClientID := "07-tendermint-666"
	mocks.MockClientKeeper.EXPECT().GetClientState(gomock.Any(), legitClientID).
		Return(&ibctmtypes.ClientState{ChainId: "provider-chain"}, true).AnyTimes()
	mocks.MockClientKeeper.EXPECT().GetClientState(gomock.Any(), rogueClientID).
		Return(&ibctmtypes.ClientState{ChainId: "attacker-chain"}, true).AnyTimes()

	pk1, err := cryptocodec.ToCmtProtoPublicKey(ed25519.GenPrivKey().PubKey())
	require.NoError(t, err)

	// A legitimate first packet establishes the pin.
	pd1 := types.NewValidatorSetChangePacketData([]abci.ValidatorUpdate{{PubKey: pk1, Power: 10}}, 1)
	require.NoError(t, k.OnRecvVSCPacketV2(ctx, legitClientID, pd1))

	prevPending, ok := k.GetPendingChanges(ctx)
	require.True(t, ok)
	prevClientID, found := k.GetProviderClientID(ctx)
	require.True(t, found)
	prevRecvTime := k.GetLastVSCRecvTime(ctx)
	prevHighestID, _, err := k.GetHighestValsetUpdateID(ctx)
	require.NoError(t, err)

	// Advance block time so a mutation to LastVSCRecvTime would be observable.
	laterCtx := ctx.WithBlockTime(ctx.BlockTime().Add(time.Hour))

	staged := types.DowntimeParams{
		SignedBlocksWindow: 12345,
		MinSignedPerWindow: math.LegacyMustNewDecFromStr("0.5"),
	}
	pd2 := types.NewValidatorSetChangePacketData([]abci.ValidatorUpdate{{PubKey: pk1, Power: 999}}, 2)
	pd2.DowntimeParams = &staged
	pd2.IsSnapshot = true
	pd2.ConsumerInDebt = true

	err = k.OnRecvVSCPacketV2(laterCtx, rogueClientID, pd2)
	require.Error(t, err, "a packet delivered over a client tracking a different chain id must be rejected")

	pending, ok := k.GetPendingChanges(ctx)
	require.True(t, ok)
	require.Equal(t, *prevPending, *pending, "valset must be unchanged by a rejected packet")

	clientID, found := k.GetProviderClientID(ctx)
	require.True(t, found)
	require.Equal(t, prevClientID, clientID, "ProviderClientID must be unchanged by a rejected packet")

	require.Equal(t, prevRecvTime, k.GetLastVSCRecvTime(laterCtx), "LastVSCRecvTime must be unchanged by a rejected packet")

	require.False(t, k.IsConsumerInDebt(ctx), "the in-debt flag must be unchanged by a rejected packet")

	_, err = k.StagedDowntimeParams.Get(ctx)
	require.Error(t, err, "nothing should be staged from a rejected packet")

	highestID, _, err := k.GetHighestValsetUpdateID(ctx)
	require.NoError(t, err)
	require.Equal(t, prevHighestID, highestID, "highest vsc id must not advance for a rejected packet")

	pinned, found := k.GetProviderChainId(ctx)
	require.True(t, found)
	require.Equal(t, "provider-chain", pinned, "the pin itself must not change")
}

// TestOnRecvVSCPacketV2SameChainIdHealsClient adapts the pre-existing
// heal regression coverage (see TestOnRecvVSCPacketV2 above and
// TestIBCModuleOnRecvPacketHealsStaleProviderClient) to the chain-id gate:
// a client replacement is allowed, and continues to heal ProviderClientID,
// as long as the replacement client tracks the SAME pinned chain id.
func TestOnRecvVSCPacketV2SameChainIdHealsClient(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()
	testkeeper.StubClientState(mocks, "provider-chain")

	staleClientID := "07-tendermint-0"
	freshClientID := "07-tendermint-1"

	pk1, err := cryptocodec.ToCmtProtoPublicKey(ed25519.GenPrivKey().PubKey())
	require.NoError(t, err)
	pd1 := types.NewValidatorSetChangePacketData([]abci.ValidatorUpdate{{PubKey: pk1, Power: 10}}, 1)
	require.NoError(t, k.OnRecvVSCPacketV2(ctx, staleClientID, pd1))

	pd2 := types.NewValidatorSetChangePacketData([]abci.ValidatorUpdate{{PubKey: pk1, Power: 20}}, 2)
	require.NoError(t, k.OnRecvVSCPacketV2(ctx, freshClientID, pd2),
		"a same-chain-id client replacement must still be accepted")

	clientID, found := k.GetProviderClientID(ctx)
	require.True(t, found)
	require.Equal(t, freshClientID, clientID, "ProviderClientID must heal to the replacement client")

	pinned, found := k.GetProviderChainId(ctx)
	require.True(t, found)
	require.Equal(t, "provider-chain", pinned, "the pinned chain id must not change on a same-chain-id heal")
}
