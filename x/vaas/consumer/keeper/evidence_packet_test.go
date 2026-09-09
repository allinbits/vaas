package keeper_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	ibcexported "github.com/cosmos/ibc-go/v10/modules/core/exported"

	channeltypesv2 "github.com/cosmos/ibc-go/v10/modules/core/04-channel/v2/types"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"cosmossdk.io/math"

	testkeeper "github.com/allinbits/vaas/testutil/keeper"
	vaastypes "github.com/allinbits/vaas/x/vaas/types"
)

// TestSendEvidencePacketsSkipsWhileClientFrozen: during an outage (frozen or
// expired pinned client) the send loop must not touch the queue at all;
// without the guard every pending entry is re-read and its SendPacket
// re-attempted every block, for the whole length of the outage.
func TestSendEvidencePacketsSkipsWhileClientFrozen(t *testing.T) {
	ck, ctx, ctrl, mocks := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	ck.SetProviderClientID(ctx, "07-tendermint-0")
	for i := int64(1); i <= 2; i++ {
		packet := vaastypes.NewEvidencePacketData(sdk.ConsAddress([]byte("val-1")), i*10, []byte{0x3F}, 8, 8, math.LegacyMustNewDecFromStr("0.5"))
		require.NoError(t, ck.QueueEvidencePacket(ctx, packet))
	}

	// No SendPacket expectation is registered, so any attempt fails the test.
	mocks.MockClientKeeper.EXPECT().GetClientStatus(gomock.Any(), "07-tendermint-0").
		Return(ibcexported.Frozen).Times(1)
	require.NoError(t, ck.SendEvidencePackets(ctx))
	require.Equal(t, 2, ck.GetPendingEvidencePacketCount(ctx), "the queue must be held, untouched")
}

// TestSendEvidencePacketsSkipsWithoutCounterparty: an Active pin with no
// registered counterparty cannot route either; same O(1) hold.
func TestSendEvidencePacketsSkipsWithoutCounterparty(t *testing.T) {
	ck, ctx, ctrl, mocks := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	ck.SetProviderClientID(ctx, "07-tendermint-0")
	packet := vaastypes.NewEvidencePacketData(sdk.ConsAddress([]byte("val-1")), 10, []byte{0x3F}, 8, 8, math.LegacyMustNewDecFromStr("0.5"))
	require.NoError(t, ck.QueueEvidencePacket(ctx, packet))

	mocks.MockClientKeeper.EXPECT().GetClientStatus(gomock.Any(), "07-tendermint-0").
		Return(ibcexported.Active).Times(1)
	// The harness's counterparty registry is empty, so the pin has none.
	require.NoError(t, ck.SendEvidencePackets(ctx))
	require.Equal(t, 1, ck.GetPendingEvidencePacketCount(ctx), "the queue must be held, untouched")
}

// TestSendEvidencePacketsCapsSendsPerBlock: a recovered client drains a large
// backlog at a bounded rate instead of issuing one SendPacket per entry in a
// single EndBlock.
func TestSendEvidencePacketsCapsSendsPerBlock(t *testing.T) {
	ck, ctx, ctrl, mocks := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	params := vaastypes.DefaultConsumerParams()
	params.Enabled = true
	ck.SetParams(ctx, params)

	ck.SetProviderClientID(ctx, "07-tendermint-0")
	for i := 0; i < 120; i++ {
		addr := sdk.ConsAddress([]byte(fmt.Sprintf("val-%03d", i)))
		packet := vaastypes.NewEvidencePacketData(addr, 10, []byte{0x3F}, 8, 8, math.LegacyMustNewDecFromStr("0.5"))
		require.NoError(t, ck.QueueEvidencePacket(ctx, packet))
	}
	require.Equal(t, 120, ck.GetPendingEvidencePacketCount(ctx))

	mocks.MockClientKeeper.EXPECT().GetClientStatus(gomock.Any(), "07-tendermint-0").
		Return(ibcexported.Active).AnyTimes()
	mocks.StubClientCounterparty("07-tendermint-0")

	for _, want := range []struct{ sends, left int }{{50, 70}, {50, 20}, {20, 0}} {
		mocks.MockChannelV2Keeper.EXPECT().SendPacket(gomock.Any(), gomock.Any()).
			Return(&channeltypesv2.MsgSendPacketResponse{Sequence: 1}, nil).Times(want.sends)
		require.NoError(t, ck.SendEvidencePackets(ctx))
		require.Equal(t, want.left, ck.GetPendingEvidencePacketCount(ctx))
	}
}

// TestQueueEvidencePacketBoundsPerValidator: the queue keeps at most the
// newest windows per validator, evicting the oldest, so an offline validator
// during a long outage cannot grow state without bound. The evicted windows
// are the ones the provider would reject as beyond its evidence age anyway.
func TestQueueEvidencePacketBoundsPerValidator(t *testing.T) {
	ck, ctx, ctrl, _ := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	addr := sdk.ConsAddress([]byte("val-bounded"))
	for i := int64(1); i <= 17; i++ {
		packet := vaastypes.NewEvidencePacketData(addr, i*10, []byte{0x3F}, 8, 8, math.LegacyMustNewDecFromStr("0.5"))
		require.NoError(t, ck.QueueEvidencePacket(ctx, packet))
	}

	require.Equal(t, 16, ck.GetPendingEvidencePacketCount(ctx),
		"the 17th window must evict one entry")
	packets := pendingPacketsFor(t, ctx, ck, addr)
	require.Len(t, packets, 16)
	ends := make([]int64, 0, len(packets))
	for _, p := range packets {
		ends = append(ends, p.WindowEndHeight)
	}
	require.NotContains(t, ends, int64(17), "the oldest window must be the one evicted")
	require.Contains(t, ends, int64(177), "the newest window must be retained")
}
