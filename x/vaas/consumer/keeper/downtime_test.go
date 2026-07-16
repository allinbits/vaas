package keeper_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/allinbits/vaas/x/vaas/consumer/keeper"
	vaastypes "github.com/allinbits/vaas/x/vaas/types"

	abci "github.com/cometbft/cometbft/abci/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"

	"cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"

	testkeeper "github.com/allinbits/vaas/testutil/keeper"
)

// countBitmaps returns the number of validators with a tracked missed-block
// bitmap in the current window.
func countBitmaps(ctx sdk.Context, k keeper.Keeper) int {
	iter, err := k.MissedBlockBitmaps.Iterate(ctx, nil)
	if err != nil {
		return 0
	}
	defer iter.Close()

	count := 0
	for ; iter.Valid(); iter.Next() {
		count++
	}
	return count
}

// setDowntimeParams sets a consumer params record with the given tumbling
// window size and minimum signed fraction, leaving the other fields at
// their defaults.
func setDowntimeParams(ctx sdk.Context, k keeper.Keeper, window int64, minSigned string) {
	params := vaastypes.DefaultConsumerParams()
	params.SignedBlocksWindow = window
	params.MinSignedPerWindow = math.LegacyMustNewDecFromStr(minSigned)
	k.SetParams(ctx, params)
}

func TestTrackMissedBlocksQueuesEvidenceAtWindowClose(t *testing.T) {
	consumerKeeper, ctx, ctrl, _ := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	// params: window 4, minSigned 0.5 => maxMissed = 4 - 2 = 2
	setDowntimeParams(ctx, consumerKeeper, 4, "0.5")

	addrA := []byte{0x0A}
	addrB := []byte{0x0B}

	// validator A misses heights 4,5,6 (3 > 2): evidence queued at close
	missedA := map[int64]bool{4: true, 5: true, 6: true}
	// validator B misses only height 4: no evidence
	missedB := map[int64]bool{4: true}

	votesForHeight := func(h int64) []abci.VoteInfo {
		flag := func(missed map[int64]bool) cmtproto.BlockIDFlag {
			if missed[h] {
				return cmtproto.BlockIDFlagAbsent
			}
			return cmtproto.BlockIDFlagCommit
		}
		return []abci.VoteInfo{
			{Validator: abci.Validator{Address: addrA, Power: 1}, BlockIdFlag: flag(missedA)},
			{Validator: abci.Validator{Address: addrB, Power: 1}, BlockIdFlag: flag(missedB)},
		}
	}

	// window [4,7]: vote for height h arrives in block h+1, so the window
	// closes in block 8 (h-1 == 7 is the last covered height)
	for h := int64(5); h <= 8; h++ {
		ctx = ctx.WithBlockHeight(h).WithVoteInfos(votesForHeight(h - 1))
		consumerKeeper.TrackMissedBlocks(ctx)
	}
	require.Equal(t, 1, consumerKeeper.GetPendingEvidencePacketCount(ctx))
	// bitmap resets after close
	require.Equal(t, 0, countBitmaps(ctx, consumerKeeper))

	// the packet was queued for the offender, not the well-behaved validator
	require.True(t, mustHasPendingPacket(t, ctx, consumerKeeper, addrA))
	require.False(t, mustHasPendingPacket(t, ctx, consumerKeeper, addrB))
}

func TestTrackMissedBlocksTrimsToFirstTrackedHeight(t *testing.T) {
	consumerKeeper, ctx, ctrl, _ := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	// window 4, minSigned 0.75 => maxMissed = 4 - 3 = 1, so a validator only
	// tracked for the last 2 heights of the window (both missed) still
	// exceeds the threshold and gets evidence scoped to its actual span.
	setDowntimeParams(ctx, consumerKeeper, 4, "0.75")

	addr := []byte{0x0C}

	// validator is absent from vote infos entirely for heights 4 and 5
	// (e.g. it only just bonded), then appears and misses heights 6 and 7.
	votesForHeight := func(h int64) []abci.VoteInfo {
		switch h {
		case 6, 7:
			return []abci.VoteInfo{
				{Validator: abci.Validator{Address: addr, Power: 1}, BlockIdFlag: cmtproto.BlockIDFlagAbsent},
			}
		default:
			return []abci.VoteInfo{}
		}
	}

	// window [4,7]: vote for height h arrives in block h+1
	for h := int64(5); h <= 8; h++ {
		ctx = ctx.WithBlockHeight(h).WithVoteInfos(votesForHeight(h - 1))
		consumerKeeper.TrackMissedBlocks(ctx)
	}

	require.Equal(t, 1, consumerKeeper.GetPendingEvidencePacketCount(ctx))

	packet := getPendingPacket(t, ctx, consumerKeeper, addr)
	// validator first seen at height 6 inside window [4,7]:
	// packet WindowStartHeight must be 6, span 2
	require.Equal(t, int64(6), packet.WindowStartHeight)
	require.Equal(t, int64(2), packet.Span())
	require.Equal(t, int64(7), packet.InfractionHeight)
	require.Equal(t, int64(2), packet.MissedCount())
}

func TestStagedDowntimeParamsActivateAtWindowBoundary(t *testing.T) {
	consumerKeeper, ctx, ctrl, _ := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	setDowntimeParams(ctx, consumerKeeper, 4, "0.5")

	addr := []byte{0x0D}
	allCommit := func(int64) []abci.VoteInfo {
		return []abci.VoteInfo{
			{Validator: abci.Validator{Address: addr, Power: 1}, BlockIdFlag: cmtproto.BlockIDFlagCommit},
		}
	}

	// process part of the window [4,7]: heights 4 and 5 (blocks 5,6)
	for h := int64(5); h <= 6; h++ {
		ctx = ctx.WithBlockHeight(h).WithVoteInfos(allCommit(h - 1))
		consumerKeeper.TrackMissedBlocks(ctx)
	}

	// staging window=8 mid-window does not change the current window
	staged := vaastypes.DowntimeParams{
		SignedBlocksWindow: 8,
		MinSignedPerWindow: math.LegacyMustNewDecFromStr("0.5"),
	}
	consumerKeeper.StageDowntimeParams(ctx, staged)

	window, _ := consumerKeeper.GetDowntimeParams(ctx)
	require.Equal(t, int64(4), window)

	// finish closing the current window [4,7] (blocks 7,8)
	for h := int64(7); h <= 8; h++ {
		ctx = ctx.WithBlockHeight(h).WithVoteInfos(allCommit(h - 1))
		consumerKeeper.TrackMissedBlocks(ctx)
	}

	// after the close of the current window the new size is in effect
	window, minSigned := consumerKeeper.GetDowntimeParams(ctx)
	require.Equal(t, int64(8), window)
	require.True(t, math.LegacyMustNewDecFromStr("0.5").Equal(minSigned))
}

func mustHasPendingPacket(t *testing.T, ctx sdk.Context, k keeper.Keeper, addr []byte) bool {
	t.Helper()
	has, err := k.PendingEvidencePackets.Has(ctx, addr)
	require.NoError(t, err)
	return has
}

func getPendingPacket(t *testing.T, ctx sdk.Context, k keeper.Keeper, addr []byte) vaastypes.EvidencePacketData {
	t.Helper()
	bz, err := k.PendingEvidencePackets.Get(ctx, addr)
	require.NoError(t, err)
	var packet vaastypes.EvidencePacketData
	require.NoError(t, json.Unmarshal(bz, &packet))
	return packet
}
