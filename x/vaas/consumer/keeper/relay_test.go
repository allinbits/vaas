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

	clientv2types "github.com/cosmos/ibc-go/v10/modules/core/02-client/v2/types"
	ibctmtypes "github.com/cosmos/ibc-go/v10/modules/light-clients/07-tendermint"

	errorsmod "cosmossdk.io/errors"
	"cosmossdk.io/math"

	testcrypto "github.com/allinbits/vaas/testutil/crypto"
	testkeeper "github.com/allinbits/vaas/testutil/keeper"
	consumertypes "github.com/allinbits/vaas/x/vaas/consumer/types"
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
	consumerKeeper.SetProviderClientID(ctx, providerClientID)
	mocks.ClientCounterparties[providerClientID] = clientv2types.CounterpartyInfo{ClientId: "07-tendermint-7"}

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

	// The pinned client is routable (it has a registered counterparty), so a
	// packet arriving over any other client is rejected outright.
	differentClientID := "07-tendermint-999"
	pd3 := types.NewValidatorSetChangePacketData(changes1, 3)
	err = consumerKeeper.OnRecvVSCPacketV2(ctx, differentClientID, pd3)
	require.Error(t, err, "a packet over a non-pinned client must be rejected")

	highestID, _, err = consumerKeeper.GetHighestValsetUpdateID(ctx)
	require.NoError(t, err)
	require.Equal(t, uint64(2), highestID, "a rejected packet must not advance the highest vsc id")
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
	consumerKeeper.SetProviderClientID(ctx, providerClientID)

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
	consumerKeeper.SetProviderClientID(ctx, providerClientID)

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
	consumerKeeper.SetProviderClientID(ctx, providerClientID)

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
	consumerKeeper.SetProviderClientID(ctx, providerClientID)

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
	consumerKeeper.SetProviderClientID(ctx, providerClientID)

	// Heartbeat packets presuppose a live validator set (a set-emptying packet
	// is rejected outright), so seed one as InitGenesis would.
	pk, err := cryptocodec.ToCmtProtoPublicKey(ed25519.GenPrivKey().PubKey())
	require.NoError(t, err)
	consumerKeeper.ApplyCCValidatorChanges(ctx, []abci.ValidatorUpdate{{PubKey: pk, Power: 10}})

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
	consumerKeeper.SetProviderClientID(ctx, providerClientID)

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
	k.SetProviderClientID(ctx, providerClientID)

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
	k.SetProviderClientID(ctx, providerClientID)

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
	k.SetProviderClientID(ctx, providerClientID)

	// Heartbeat packets presuppose a live validator set (a set-emptying packet
	// is rejected outright), so seed one as InitGenesis would.
	pk, err := cryptocodec.ToCmtProtoPublicKey(ed25519.GenPrivKey().PubKey())
	require.NoError(t, err)
	k.ApplyCCValidatorChanges(ctx, []abci.ValidatorUpdate{{PubKey: pk, Power: 10}})

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

// TestEmptySnapshotRejected seeds {A, B} and delivers snapshots whose target
// set is empty -- one with zero updates, one with only zero-power updates.
// Both must be rejected before any state change: applying them would remove
// every validator and halt the chain at the next EndBlock flush.
func TestEmptySnapshotRejected(t *testing.T) {
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
	k.SetLastVSCRecvTime(ctx, ctx.BlockTime())

	// Advance block time so a mutation to LastVSCRecvTime would be observable.
	laterCtx := ctx.WithBlockTime(ctx.BlockTime().Add(time.Hour))

	snapNoUpdates := types.NewValidatorSetChangePacketData(nil, 2)
	snapNoUpdates.IsSnapshot = true
	err = k.OnRecvVSCPacketV2(laterCtx, "07-tendermint-0", snapNoUpdates)
	require.Error(t, err, "a snapshot with no updates must be rejected")
	require.True(t, errorsmod.IsOf(err, consumertypes.ErrEmptyValidatorSet))

	snapAllZero := types.NewValidatorSetChangePacketData([]abci.ValidatorUpdate{
		{PubKey: pkA, Power: 0},
		{PubKey: pkB, Power: 0},
	}, 2)
	snapAllZero.IsSnapshot = true
	err = k.OnRecvVSCPacketV2(laterCtx, "07-tendermint-0", snapAllZero)
	require.Error(t, err, "a snapshot with only zero-power updates must be rejected")
	require.True(t, errorsmod.IsOf(err, consumertypes.ErrEmptyValidatorSet))

	// The rejection happens before any state change.
	_, ok := k.GetPendingChanges(ctx)
	require.False(t, ok, "a rejected packet must not stage pending changes")
	highestID, _, err := k.GetHighestValsetUpdateID(ctx)
	require.NoError(t, err)
	require.Equal(t, uint64(1), highestID, "a rejected packet must not advance the highest vsc id")
	require.Equal(t, ctx.BlockTime(), k.GetLastVSCRecvTime(laterCtx),
		"a rejected packet must not advance the staleness clock")
}

// TestDiffCannotEmptyValidatorSet exercises the same guard for diff packets:
// a diff that would remove every validator (alone or combined with the
// already-accumulated pending changes) is exactly as fatal as an empty
// snapshot and must be rejected, while an empty heartbeat diff and a diff
// that leaves at least one validator standing keep flowing.
func TestDiffCannotEmptyValidatorSet(t *testing.T) {
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

	// An empty heartbeat diff over a live set is untouched by the guard.
	heartbeat := types.NewValidatorSetChangePacketData(nil, 2)
	require.NoError(t, k.OnRecvVSCPacketV2(ctx, "07-tendermint-0", heartbeat),
		"an empty heartbeat diff over a non-empty set must be accepted")

	// A diff removing the only validator would empty the set: rejected.
	removeAll := types.NewValidatorSetChangePacketData([]abci.ValidatorUpdate{{PubKey: pkA, Power: 0}}, 3)
	err = k.OnRecvVSCPacketV2(ctx, "07-tendermint-0", removeAll)
	require.Error(t, err, "a diff removing every validator must be rejected")
	require.True(t, errorsmod.IsOf(err, consumertypes.ErrEmptyValidatorSet))
	highestID, _, err := k.GetHighestValsetUpdateID(ctx)
	require.NoError(t, err)
	require.Equal(t, uint64(2), highestID, "a rejected diff must not advance the highest vsc id")

	// Pending changes count: a diff adding B, followed by a diff zeroing both
	// A and B, would still empty the resulting set even though B never made it
	// into the applied cross-chain set.
	addB := types.NewValidatorSetChangePacketData([]abci.ValidatorUpdate{{PubKey: pkB, Power: 5}}, 3)
	require.NoError(t, k.OnRecvVSCPacketV2(ctx, "07-tendermint-0", addB))

	removeBoth := types.NewValidatorSetChangePacketData([]abci.ValidatorUpdate{
		{PubKey: pkA, Power: 0},
		{PubKey: pkB, Power: 0},
	}, 4)
	err = k.OnRecvVSCPacketV2(ctx, "07-tendermint-0", removeBoth)
	require.Error(t, err, "a diff emptying the set together with pending changes must be rejected")
	require.True(t, errorsmod.IsOf(err, consumertypes.ErrEmptyValidatorSet))

	// Removing A alone leaves the pending B standing: accepted.
	removeA := types.NewValidatorSetChangePacketData([]abci.ValidatorUpdate{{PubKey: pkA, Power: 0}}, 4)
	require.NoError(t, k.OnRecvVSCPacketV2(ctx, "07-tendermint-0", removeA),
		"a diff leaving at least one validator standing must be accepted")
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
	k.SetProviderClientID(ctx, clientID)
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

	// The genesis-established client pin; a legitimate first packet over it
	// establishes the chain-id pin.
	k.SetProviderClientID(ctx, legitClientID)
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

// TestOnRecvVSCPacketV2MissingPinRejects verifies a VSC packet is rejected
// outright when no provider client is pinned at all: both genesis paths
// establish the pin, so its absence means a malformed genesis or corrupted
// state, and the consumer must fail closed rather than adopt whatever client
// the first packet happens to arrive on.
func TestOnRecvVSCPacketV2MissingPinRejects(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()
	testkeeper.StubClientState(mocks, "provider-chain")

	pk1, err := cryptocodec.ToCmtProtoPublicKey(ed25519.GenPrivKey().PubKey())
	require.NoError(t, err)
	pd := types.NewValidatorSetChangePacketData([]abci.ValidatorUpdate{{PubKey: pk1, Power: 10}}, 1)

	err = k.OnRecvVSCPacketV2(ctx, "07-tendermint-1", pd)
	require.Error(t, err, "a VSC packet with no pin established must be rejected")
	require.Contains(t, err.Error(), "no provider client pinned")

	_, found := k.GetProviderClientID(ctx)
	require.False(t, found, "a rejected packet must not establish a pin")
	_, found2, err := k.GetHighestValsetUpdateID(ctx)
	require.NoError(t, err)
	require.False(t, found2, "a rejected packet must not be processed")
}

// TestOnRecvVSCPacketV2RejectsNonPinnedClientBeforeStateChanges verifies the
// pin gate fires before any state mutation: a same-chain-id packet over a
// client other than the (routable) pinned one leaves the valset, the pin, the
// staleness clock, the debt flag, staged params, and the vsc-id watermark all
// exactly as they were.
func TestOnRecvVSCPacketV2RejectsNonPinnedClientBeforeStateChanges(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()
	testkeeper.StubClientState(mocks, "provider-chain")

	pinnedClientID := "07-tendermint-1"
	rogueClientID := "07-tendermint-666"
	k.SetProviderClientID(ctx, pinnedClientID)
	mocks.ClientCounterparties[pinnedClientID] = clientv2types.CounterpartyInfo{ClientId: "07-tendermint-9"}

	pk1, err := cryptocodec.ToCmtProtoPublicKey(ed25519.GenPrivKey().PubKey())
	require.NoError(t, err)

	pd1 := types.NewValidatorSetChangePacketData([]abci.ValidatorUpdate{{PubKey: pk1, Power: 10}}, 1)
	require.NoError(t, k.OnRecvVSCPacketV2(ctx, pinnedClientID, pd1))

	prevPending, ok := k.GetPendingChanges(ctx)
	require.True(t, ok)
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
	require.Error(t, err, "a packet over a non-pinned client must be rejected")

	pending, ok := k.GetPendingChanges(ctx)
	require.True(t, ok)
	require.Equal(t, *prevPending, *pending, "valset must be unchanged by a rejected packet")

	clientID, found := k.GetProviderClientID(ctx)
	require.True(t, found)
	require.Equal(t, pinnedClientID, clientID, "the pin must be unchanged by a rejected packet")

	require.Equal(t, prevRecvTime, k.GetLastVSCRecvTime(laterCtx), "LastVSCRecvTime must be unchanged by a rejected packet")

	require.False(t, k.IsConsumerInDebt(ctx), "the in-debt flag must be unchanged by a rejected packet")

	_, err = k.StagedDowntimeParams.Get(ctx)
	require.Error(t, err, "nothing should be staged from a rejected packet")

	highestID, _, err := k.GetHighestValsetUpdateID(ctx)
	require.NoError(t, err)
	require.Equal(t, prevHighestID, highestID, "highest vsc id must not advance for a rejected packet")
}

// testProviderClientID is the client every test in this file delivers VSC
// packets over. Production pins a provider client at genesis on both paths (a
// new chain creates one, a restart restores the exported one), so a keeper that
// has not pinned anything is a state these tests should not start from.
const testProviderClientID = "07-tendermint-0"

// TestOnRecvVSCPacketRejectsClientOverrideOnceRoutable verifies that a second
// IBC client cannot take over as the provider client once a routable one is
// established.
//
// The chain-id gate alone does not close this: IBC has no notion of chain-id
// uniqueness, so anyone can stand up a chain that reports the provider's chain
// id, create a client for it on the consumer, register a counterparty, and
// deliver a VSC packet. The chain id then matches the pin while the validator
// set behind it does not. Left to overwrite the pin, that packet redirects the
// consumer's evidence packets to a chain that simply drops them -- silently
// disabling downtime reporting -- and a large valset_update_id in the same
// packet strands the real provider below the dedup watermark forever.
//
// A registered counterparty is the discriminator rather than the client's
// status: counterparties cannot be unregistered, so a pin that has one is
// routable for good and no expiry or freeze reopens the override.
func TestOnRecvVSCPacketRejectsClientOverrideOnceRoutable(t *testing.T) {
	const (
		establishedClientID = "07-tendermint-0"
		attackerClientID    = "07-tendermint-9"
		providerChainID     = "provider-0"
	)

	pk, err := cryptocodec.ToCmtProtoPublicKey(ed25519.GenPrivKey().PubKey())
	require.NoError(t, err)

	consumerKeeper, ctx, ctrl, mocks := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()
	// Both clients report the provider's chain id: that is the attacker's whole
	// trick, and it is why the chain-id gate lets the packet through.
	testkeeper.StubClientState(mocks, providerChainID)

	consumerKeeper.SetProviderClientID(ctx, establishedClientID)
	mocks.StubClientCounterparty(establishedClientID)

	attack := types.NewValidatorSetChangePacketData(
		[]abci.ValidatorUpdate{{PubKey: pk, Power: 1}}, 999999)
	err = consumerKeeper.OnRecvVSCPacketV2(ctx, attackerClientID, attack)
	require.ErrorIs(t, err, consumertypes.ErrInvalidProviderClient)

	pinned, found := consumerKeeper.GetProviderClientID(ctx)
	require.True(t, found)
	require.Equal(t, establishedClientID, pinned, "the established provider client must not be overridden")

	_, hasPending := consumerKeeper.GetPendingChanges(ctx)
	require.False(t, hasPending, "the rejected packet must not stage any validator changes")

	highest, found, err := consumerKeeper.GetHighestValsetUpdateID(ctx)
	require.NoError(t, err)
	require.False(t, found, "the rejected packet must not advance the dedup watermark")
	require.Zero(t, highest)

	chainID, found := consumerKeeper.GetProviderChainId(ctx)
	require.True(t, found)
	require.Equal(t, providerChainID, chainID, "the pinned chain id must survive the rejection")
}

// TestOnRecvVSCPacketRejectsWhenNoClientPinned verifies the fail-closed path:
// a consumer with no pinned provider client rejects VSC packets rather than
// letting one establish the pin.
//
// Both genesis paths pin a client -- a new chain creates one and pins it, a
// restart restores the exported pin -- so reaching this state means a malformed
// genesis or corrupted store. Accepting the packet there would hand the pin to
// whoever delivered first, which is the override this gate exists to prevent.
func TestOnRecvVSCPacketRejectsWhenNoClientPinned(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()
	testkeeper.StubClientState(mocks, "provider-0")

	_, found := k.GetProviderClientID(ctx)
	require.False(t, found, "fixture must start with no pin")

	pk, err := cryptocodec.ToCmtProtoPublicKey(ed25519.GenPrivKey().PubKey())
	require.NoError(t, err)
	pd := types.NewValidatorSetChangePacketData([]abci.ValidatorUpdate{{PubKey: pk, Power: 1}}, 1)

	require.ErrorIs(t, k.OnRecvVSCPacketV2(ctx, testProviderClientID, pd),
		consumertypes.ErrInvalidProviderClient)

	_, found = k.GetProviderClientID(ctx)
	require.False(t, found, "a rejected packet must not establish the pin")
}
