package keeper_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	abci "github.com/cometbft/cometbft/abci/types"

	clienttypes "github.com/cosmos/ibc-go/v10/modules/core/02-client/types"

	"github.com/allinbits/vaas/testutil/crypto"
	testkeeper "github.com/allinbits/vaas/testutil/keeper"
	providertypes "github.com/allinbits/vaas/x/vaas/provider/types"
	vaastypes "github.com/allinbits/vaas/x/vaas/types"
)

// TestOnAcknowledgementPacketV2 tests the IBC v2 acknowledgement handler.
// A successful ack refreshes liveness; an error ack keeps the consumer (removal
// is driven by the grace sweep, not the callback).
func TestOnAcknowledgementPacketV2(t *testing.T) {
	providerKeeper, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerId := uint64(0)
	clientId := "07-tendermint-0"

	// Setup consumer with client mapping
	providerKeeper.SetConsumerClientId(ctx, consumerId, clientId)
	providerKeeper.SetConsumerPhase(ctx, consumerId, providertypes.CONSUMER_PHASE_LAUNCHED)
	providerKeeper.SetConsumerChainId(ctx, consumerId, "consumer-chain")

	// Test 1: Success acknowledgement (empty error) - refreshes liveness, no removal
	err := providerKeeper.OnAcknowledgementPacketV2(ctx, clientId, 1, "")
	require.NoError(t, err)

	phase := providerKeeper.GetConsumerPhase(ctx, consumerId)
	require.Equal(t, providertypes.CONSUMER_PHASE_LAUNCHED, phase)

	last, found := providerKeeper.GetConsumerLastLivenessTime(ctx, consumerId)
	require.True(t, found)
	require.Equal(t, ctx.BlockTime(), last)

	// Test 2: Error acknowledgement - consumer is NOT removed (kept, liveness not
	// refreshed); the grace sweep is the only removal path.
	err = providerKeeper.OnAcknowledgementPacketV2(ctx, clientId, 1, "packet data could not be decoded")
	require.NoError(t, err)

	phase = providerKeeper.GetConsumerPhase(ctx, consumerId)
	require.Equal(t, providertypes.CONSUMER_PHASE_LAUNCHED, phase)
}

// TestOnAcknowledgementPacketV2UnknownClient tests error handling for unknown clients.
func TestOnAcknowledgementPacketV2UnknownClient(t *testing.T) {
	providerKeeper, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	unknownClientId := "07-tendermint-999"

	// Error ack with unknown client should return error
	err := providerKeeper.OnAcknowledgementPacketV2(ctx, unknownClientId, 1, "some error")
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown client")
}

// TestOnTimeoutPacketV2 tests the IBC v2 timeout handler. A timeout is a liveness
// signal, not misbehaviour: the consumer is kept (removal is the grace sweep's job).
func TestOnTimeoutPacketV2(t *testing.T) {
	providerKeeper, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerId := uint64(0)
	clientId := "07-tendermint-0"

	// Setup consumer with client mapping
	providerKeeper.SetConsumerClientId(ctx, consumerId, clientId)
	providerKeeper.SetConsumerPhase(ctx, consumerId, providertypes.CONSUMER_PHASE_LAUNCHED)
	providerKeeper.SetConsumerChainId(ctx, consumerId, "consumer-chain")

	// Timeout should NOT remove the consumer.
	err := providerKeeper.OnTimeoutPacketV2(ctx, clientId)
	require.NoError(t, err)

	phase := providerKeeper.GetConsumerPhase(ctx, consumerId)
	require.Equal(t, providertypes.CONSUMER_PHASE_LAUNCHED, phase)
}

// TestOnTimeoutPacketV2UnknownClient tests error handling for unknown clients.
func TestOnTimeoutPacketV2UnknownClient(t *testing.T) {
	providerKeeper, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	unknownClientId := "07-tendermint-999"

	// Timeout with unknown client should return error
	err := providerKeeper.OnTimeoutPacketV2(ctx, unknownClientId)
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown client")
}

// TestBeginBlockRemoveUnresponsiveConsumers verifies the liveness grace sweep:
// a consumer is removed only once it has had no successful ack for longer than
// the derived grace period.
func TestBeginBlockRemoveUnresponsiveConsumers(t *testing.T) {
	providerKeeper, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	// 21d unbonding -> grace = 0.33 * 21d ~= 6.93d. Called by both the grace
	// computation and StopAndPrepareForConsumerRemoval.
	mocks.MockStakingKeeper.EXPECT().UnbondingTime(gomock.Any()).Return(time.Hour*24*21, nil).AnyTimes()

	clientId := "07-tendermint-0"
	consumerId := providerKeeper.FetchAndIncrementConsumerId(ctx)
	providerKeeper.SetConsumerClientId(ctx, consumerId, clientId)
	providerKeeper.SetConsumerPhase(ctx, consumerId, providertypes.CONSUMER_PHASE_LAUNCHED)
	providerKeeper.SetConsumerChainId(ctx, consumerId, "consumer-chain")

	// Within grace: last ack 1 hour ago -> kept.
	require.NoError(t, providerKeeper.SetConsumerLastLivenessTime(ctx, consumerId, ctx.BlockTime().Add(-time.Hour)))
	require.NoError(t, providerKeeper.BeginBlockRemoveUnresponsiveConsumers(ctx))
	require.Equal(t, providertypes.CONSUMER_PHASE_LAUNCHED, providerKeeper.GetConsumerPhase(ctx, consumerId))

	// Beyond grace: last ack 30 days ago -> removed.
	require.NoError(t, providerKeeper.SetConsumerLastLivenessTime(ctx, consumerId, ctx.BlockTime().Add(-30*24*time.Hour)))
	require.NoError(t, providerKeeper.BeginBlockRemoveUnresponsiveConsumers(ctx))
	require.Equal(t, providertypes.CONSUMER_PHASE_STOPPED, providerKeeper.GetConsumerPhase(ctx, consumerId))
}

// TestAdvanceAckedBaselineOnAck verifies that a successful ack for the latest-sent
// vsc id advances the acknowledged baseline to the latest-sent snapshot, while an
// ack for a stale id leaves the baseline untouched.
func TestAdvanceAckedBaselineOnAck(t *testing.T) {
	providerKeeper, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerId := uint64(0)
	clientId := "07-tendermint-0"
	providerKeeper.SetConsumerClientId(ctx, consumerId, clientId)
	providerKeeper.SetConsumerPhase(ctx, consumerId, providertypes.CONSUMER_PHASE_LAUNCHED)
	providerKeeper.SetConsumerChainId(ctx, consumerId, "consumer-chain")

	ci := crypto.NewCryptoIdentityFromIntSeed(1)
	pk := ci.TMProtoCryptoPublicKey()
	latestSent := []providertypes.ConsensusValidator{{
		ProviderConsAddr: ci.SDKValConsAddress(),
		Power:            100,
		PublicKey:        &pk,
	}}
	require.NoError(t, providerKeeper.SetLatestSentConsumerValSet(ctx, consumerId, latestSent))
	require.NoError(t, providerKeeper.SetConsumerLatestSentVscId(ctx, consumerId, 5))

	// Ack for a stale id: baseline must NOT advance.
	require.NoError(t, providerKeeper.OnAcknowledgementPacketV2(ctx, clientId, 4, ""))
	baseline, err := providerKeeper.GetAckedConsumerValSet(ctx, consumerId)
	require.NoError(t, err)
	require.Empty(t, baseline)

	// Ack for the latest-sent id: baseline advances to the latest-sent snapshot.
	require.NoError(t, providerKeeper.OnAcknowledgementPacketV2(ctx, clientId, 5, ""))
	baseline, err = providerKeeper.GetAckedConsumerValSet(ctx, consumerId)
	require.NoError(t, err)
	require.Len(t, baseline, 1)
	require.Equal(t, latestSent[0].ProviderConsAddr, baseline[0].ProviderConsAddr)
	require.Equal(t, int64(100), baseline[0].Power)
}

// TestClientIdToConsumerIdMapping tests the client ID to consumer ID mapping used in IBC v2.
func TestClientIdToConsumerIdMapping(t *testing.T) {
	providerKeeper, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerId := uint64(1)
	clientId := "07-tendermint-0"

	// Initially no mapping
	_, found := providerKeeper.GetClientIdToConsumerId(ctx, clientId)
	require.False(t, found)

	// Set the mapping via SetConsumerClientId (which sets both directions)
	providerKeeper.SetConsumerClientId(ctx, consumerId, clientId)

	// Verify forward mapping
	gotClientId, found := providerKeeper.GetConsumerClientId(ctx, consumerId)
	require.True(t, found)
	require.Equal(t, clientId, gotClientId)

	// Verify reverse mapping
	gotConsumerId, found := providerKeeper.GetClientIdToConsumerId(ctx, clientId)
	require.True(t, found)
	require.Equal(t, consumerId, gotConsumerId)

	// Delete and verify both mappings are removed
	providerKeeper.DeleteConsumerClientId(ctx, consumerId)

	_, found = providerKeeper.GetConsumerClientId(ctx, consumerId)
	require.False(t, found)
	_, found = providerKeeper.GetClientIdToConsumerId(ctx, clientId)
	require.False(t, found)
}

// TestSendVSCPacketsToChainNoHandler tests that SendVSCPacketsToChain gracefully
// handles the case when the IBC v2 channel keeper returns ErrClientNotActive.
func TestSendVSCPacketsToChainNoHandler(t *testing.T) {
	providerKeeper, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerId := uint64(0)
	clientId := "07-tendermint-0"

	// Setup consumer with pending packets
	providerKeeper.SetConsumerClientId(ctx, consumerId, clientId)
	providerKeeper.SetConsumerPhase(ctx, consumerId, providertypes.CONSUMER_PHASE_LAUNCHED)

	// Add a pending VSC packet
	providerKeeper.AppendPendingVSCPackets(ctx, consumerId, vaastypes.ValidatorSetChangePacketData{
		ValidatorUpdates: []abci.ValidatorUpdate{},
		ValsetUpdateId:   1,
	})

	// Simulate an inactive client so packets are not sent
	mocks.MockChannelV2Keeper.EXPECT().
		SendPacket(gomock.Any(), gomock.Any()).
		Return(nil, clienttypes.ErrClientNotActive)

	err := providerKeeper.SendVSCPacketsToChain(ctx, consumerId, clientId)
	require.NoError(t, err)

	// Pending packets should still be there since the client was not active
	pending := providerKeeper.GetPendingVSCPackets(ctx, consumerId)
	require.Len(t, pending, 1)
}
