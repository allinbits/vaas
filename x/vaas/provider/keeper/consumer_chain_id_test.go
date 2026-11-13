package keeper_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	abci "github.com/cometbft/cometbft/abci/types"

	clienttypes "github.com/cosmos/ibc-go/v10/modules/core/02-client/types"
	channeltypesv2 "github.com/cosmos/ibc-go/v10/modules/core/04-channel/v2/types"
	ibcexported "github.com/cosmos/ibc-go/v10/modules/core/exported"
	ibctmtypes "github.com/cosmos/ibc-go/v10/modules/light-clients/07-tendermint"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"

	testkeeper "github.com/allinbits/vaas/testutil/keeper"
	providerkeeper "github.com/allinbits/vaas/x/vaas/provider/keeper"
	"github.com/allinbits/vaas/x/vaas/provider/types"
	vaastypes "github.com/allinbits/vaas/x/vaas/types"
)

const (
	chainIDTestClient = "07-tendermint-0"
	chainIDTestOld    = "consumer-1"
	chainIDTestNew    = "consumer-2"
)

// launchedWithRecoveredClient seeds a launched consumer with a chain id and a declared
// client, and makes the client report the given chain id at the given height.
func launchedWithRecoveredClient(t *testing.T, k providerkeeper.Keeper, ctx sdk.Context, mocks testkeeper.MockedKeepers) uint64 {
	t.Helper()
	clientChainID := chainIDTestNew
	const height = 500
	cid := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, cid, types.CONSUMER_PHASE_LAUNCHED)
	k.SetConsumerChainId(ctx, cid, chainIDTestOld)
	k.SetConsumerOwnerAddress(ctx, cid, declOwner)
	k.SetConsumerClientId(ctx, cid, chainIDTestClient)
	k.SetInfractionParams(ctx, types.DefaultInfractionParameters())
	require.NoError(t, k.SetConsumerInitializationParameters(ctx, cid, types.ConsumerInitializationParameters{
		InitialHeight: clienttypes.NewHeight(1, 1), SpawnTime: time.Unix(1_700_000_000, 0).UTC(),
	}))
	mocks.MockClientKeeper.EXPECT().GetClientState(gomock.Any(), chainIDTestClient).Return(&ibctmtypes.ClientState{
		ChainId:        clientChainID,
		TrustingPeriod: 14 * 24 * time.Hour,
		LatestHeight:   clienttypes.NewHeight(clienttypes.ParseChainID(clientChainID), height),
	}, true).AnyTimes()
	return cid
}

// TestSendVSCPacketsSkipsAConsumerWhoseClientTracksAnotherChainId: in the
// block in which the client switches, before the next BeginBlock pauses the
// consumer, nothing is sent over it. The packet stays queued, no send is
// attempted (gomock would fail on one), and the gate emits nothing: the
// mismatch event belongs to the pause.
func TestSendVSCPacketsSkipsAConsumerWhoseClientTracksAnotherChainId(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()
	cid := launchedWithRecoveredClient(t, k, ctx, mocks)
	k.AppendPendingVSCPackets(ctx, cid, vaastypes.ValidatorSetChangePacketData{
		ValidatorUpdates: []abci.ValidatorUpdate{}, ValsetUpdateId: 1,
	})

	require.NoError(t, k.SendVSCPackets(ctx))
	require.NoError(t, k.SendVSCPackets(ctx))

	require.Len(t, k.GetPendingVSCPackets(ctx, cid), 1, "the packet stays queued")
	require.Equal(t, 0, countEvents(ctx, types.EventTypeConsumerClientChainIdMismatch))
}

// TestHandleConsumerDowntimeRefusesAConsumerWhoseClientTracksAnotherChainId:
// an accusation the accused could not disprove (no header the recovered
// client verifies is signed over the consumer's chain id) is refused, not
// accepted undisprovable.
func TestHandleConsumerDowntimeRefusesAConsumerWhoseClientTracksAnotherChainId(t *testing.T) {
	windowEndTime := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	k, ctx, ctrl, _, cid, providerAddr := setupDowntimeTest(t, downtimeParams(8, "0.5", 0, 7*24*time.Hour, 72*time.Hour), windowEndTime.Add(-30*24*time.Hour), windowEndTime)
	defer ctrl.Finish()
	ctx = ctx.WithBlockTime(windowEndTime)
	// The fixture's client tracks "consumer-chain"; the consumer is renamed
	// under it, as a recovery onto the consumer's new chain id leaves it.
	k.SetConsumerChainId(ctx, cid, "consumer-chain-2")

	evidence := vaastypes.NewEvidencePacketData(providerAddr.ToSdkConsAddr(), 93, []byte{0x3F}, 8, 8, math.LegacyMustNewDecFromStr("0.5"))
	err := k.HandleConsumerEvidencePacket(ctx, cid, evidence)
	require.ErrorIs(t, err, vaastypes.ErrInvalidConsumerState)
	require.ErrorContains(t, err, "tracks chain id consumer-chain")
	_, err = k.PendingDowntimeSlashes.Get(ctx, collections.Join3(cid, providerAddr.ToSdkConsAddr().Bytes(), int64(100)))
	require.ErrorIs(t, err, collections.ErrNotFound, "nothing may be queued for a refused accusation")
}

// TestHandleConsumerDowntimeRefusesAWindowFromBeforeARename: once renamed,
// a consumer cannot accuse a validator over a window reaching back to the
// heights sealed under its old chain id (at or below the client's height at
// the rename), which no header the recovered client verifies could disprove;
// a window starting right above the boundary is accepted as before.
func TestHandleConsumerDowntimeRefusesAWindowFromBeforeARename(t *testing.T) {
	windowEndTime := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	k, ctx, ctrl, mocks, cid, providerAddr := setupDowntimeTest(t, downtimeParams(8, "0.5", 0, 7*24*time.Hour, 72*time.Hour), windowEndTime.Add(-30*24*time.Hour), windowEndTime)
	defer ctrl.Finish()
	ctx = ctx.WithBlockTime(windowEndTime)
	require.NoError(t, k.ConsumerPreviousChainIds.Set(ctx, collections.Join(cid, "consumer-chain-0"), clienttypes.NewHeight(0, 93)))
	consAddr := providerAddr.ToSdkConsAddr()

	err := k.HandleConsumerEvidencePacket(ctx, cid, vaastypes.NewEvidencePacketData(consAddr, 93, []byte{0x3F}, 8, 8, math.LegacyMustNewDecFromStr("0.5")))
	require.ErrorIs(t, err, vaastypes.ErrInvalidPacketData)
	require.ErrorContains(t, err, "before its rename")
	_, err = k.PendingDowntimeSlashes.Get(ctx, collections.Join3(cid, consAddr.Bytes(), int64(100)))
	require.ErrorIs(t, err, collections.ErrNotFound)

	k.SetEpochShareRecord(ctx, cid, windowEndTime, math.NewInt(1000))
	mocks.MockPhotonKeeper.EXPECT().ConversionRate(ctx).Return(math.LegacyNewDec(2), nil)
	require.NoError(t, k.HandleConsumerEvidencePacket(ctx, cid, vaastypes.NewEvidencePacketData(consAddr, 94, []byte{0x3F}, 8, 8, math.LegacyMustNewDecFromStr("0.5"))))
	_, err = k.PendingDowntimeSlashes.Get(ctx, collections.Join3(cid, consAddr.Bytes(), int64(101)))
	require.NoError(t, err, "a window starting above the boundary is accepted")
}

// TestPauseConsumersWithMismatchedClients: the BeginBlock mirror pauses a
// launched consumer whose declared client tracks another chain id, under the
// mismatch reason, and discards what nobody could disprove any more: the
// matured pending slash, the epoch mark and the withheld fee record are gone
// before the sweeps that follow it in BeginBlock run (no staking
// expectation is set, so an execution would fail on the mock). The mismatch
// event fires once, with the pause; consumers whose client agrees, or that
// have none, are left alone.
func TestPauseConsumersWithMismatchedClients(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()
	ctx = ctx.WithBlockTime(time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC))
	cid := launchedWithRecoveredClient(t, k, ctx, mocks)

	agreeing := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, agreeing, types.CONSUMER_PHASE_LAUNCHED)
	k.SetConsumerChainId(ctx, agreeing, "agreeing-1")
	k.SetConsumerClientId(ctx, agreeing, "07-tendermint-1")
	mocks.MockClientKeeper.EXPECT().GetClientState(gomock.Any(), "07-tendermint-1").Return(&ibctmtypes.ClientState{ChainId: "agreeing-1"}, true).AnyTimes()
	clientless := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, clientless, types.CONSUMER_PHASE_LAUNCHED)
	k.SetConsumerChainId(ctx, clientless, "clientless-1")

	_, accused := newSweepValidator(t)
	consAddr := accused.ToSdkConsAddr()
	putPendingDowntimeSlash(t, k, ctx, cid, accused, math.NewInt(100), ctx.BlockTime().Add(-time.Minute), 100)
	k.MarkEpochDowntime(ctx, cid, consAddr)
	recordKey := collections.Join(cid, consAddr.Bytes())
	require.NoError(t, k.WithheldFeeRecords.Set(ctx, recordKey, types.WithheldFeeRecord{
		ConsumerId: cid, ProviderConsAddr: consAddr.Bytes(), Amount: sdk.NewInt64Coin("uphoton", 5), ExpiresAt: ctx.BlockTime().Add(time.Hour),
	}))
	// An empty pool: the record is repaid as far as the pool goes, which is nothing.
	mocks.MockBankKeeper.EXPECT().GetBalance(gomock.Any(), k.GetConsumerFeePoolAddress(cid), "uphoton").Return(sdk.NewInt64Coin("uphoton", 0))

	k.PauseConsumersWithMismatchedClients(ctx)

	require.Equal(t, types.CONSUMER_PHASE_PAUSED, k.GetConsumerPhase(ctx, cid))
	require.Equal(t, types.PAUSE_REASON_CLIENT_CHAIN_ID_MISMATCH, k.GetConsumerPauseReason(ctx, cid))
	autoStop, err := k.GetConsumerPauseExpirationTime(ctx, cid)
	require.NoError(t, err)
	require.Equal(t, ctx.BlockTime().Add(k.GetMaxPauseDuration(ctx)), autoStop)
	has, err := k.PendingDowntimeSlashes.Has(ctx, collections.Join3(cid, consAddr.Bytes(), int64(100)))
	require.NoError(t, err)
	require.False(t, has, "the pending downtime slash is discarded")
	require.False(t, k.IsEpochDowntime(ctx, cid, consAddr), "the epoch downtime mark is discarded")
	has, err = k.WithheldFeeRecords.Has(ctx, recordKey)
	require.NoError(t, err)
	require.False(t, has, "the withheld fee record is repaid")
	require.Equal(t, types.CONSUMER_PHASE_LAUNCHED, k.GetConsumerPhase(ctx, agreeing))
	require.Equal(t, types.CONSUMER_PHASE_LAUNCHED, k.GetConsumerPhase(ctx, clientless))

	var mismatch []sdk.Event
	for _, ev := range ctx.EventManager().Events() {
		if ev.Type == types.EventTypeConsumerClientChainIdMismatch {
			mismatch = append(mismatch, ev)
		}
	}
	require.Len(t, mismatch, 1)
	attrs := map[string]string{}
	for _, a := range mismatch[0].Attributes {
		attrs[a.Key] = a.Value
	}
	require.Equal(t, chainIDTestClient, attrs[types.AttributeConsumerClientID])
	require.Equal(t, chainIDTestNew, attrs[types.AttributeClientChainId])
	require.Equal(t, chainIDTestOld, attrs[types.AttributeConsumerChainId])
	require.Equal(t, 1, countEvents(ctx, vaastypes.EventTypeConsumerPaused))

	// Once per detection: the paused consumer is not looked at again.
	k.PauseConsumersWithMismatchedClients(ctx)
	require.Equal(t, 1, countEvents(ctx, types.EventTypeConsumerClientChainIdMismatch))

	k.SweepPendingDowntimeSlashes(ctx)
	k.SweepExpiredWithheldFeeRecords(ctx)
}

// pausedForMismatch seeds a launched consumer whose declared client the test
// can switch between chain ids, pauses it through the BeginBlock mirror and
// returns it with a pointer to the client state the client keeper serves.
func pausedForMismatch(t *testing.T, k providerkeeper.Keeper, ctx sdk.Context, mocks testkeeper.MockedKeepers) (uint64, *ibctmtypes.ClientState) {
	t.Helper()
	cid := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, cid, types.CONSUMER_PHASE_LAUNCHED)
	k.SetConsumerChainId(ctx, cid, chainIDTestOld)
	k.SetConsumerOwnerAddress(ctx, cid, declOwner)
	k.SetConsumerClientId(ctx, cid, chainIDTestClient)
	k.SetInfractionParams(ctx, types.DefaultInfractionParameters())
	require.NoError(t, k.SetConsumerInitializationParameters(ctx, cid, types.ConsumerInitializationParameters{
		InitialHeight: clienttypes.NewHeight(1, 1), SpawnTime: time.Unix(1_700_000_000, 0).UTC(),
	}))
	clientState := &ibctmtypes.ClientState{
		ChainId:        chainIDTestNew,
		TrustingPeriod: 14 * 24 * time.Hour,
		LatestHeight:   clienttypes.NewHeight(2, 500),
	}
	mocks.MockClientKeeper.EXPECT().GetClientState(gomock.Any(), chainIDTestClient).
		DoAndReturn(func(sdk.Context, string) (ibcexported.ClientState, bool) { return clientState, true }).AnyTimes()
	mocks.MockClientKeeper.EXPECT().GetClientStatus(gomock.Any(), chainIDTestClient).Return(ibcexported.Active).AnyTimes()
	k.PauseConsumersWithMismatchedClients(ctx)
	require.Equal(t, types.PAUSE_REASON_CLIENT_CHAIN_ID_MISMATCH, k.GetConsumerPauseReason(ctx, cid))
	return cid, clientState
}

// TestAMismatchPauseEndsWithARenameAndAResume: the first exit. The resume
// is refused while the mismatch holds; governance renames the paused
// consumer to its client's chain id and resumes it.
func TestAMismatchPauseEndsWithARenameAndAResume(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()
	msgServer := providerkeeper.NewMsgServerImpl(&k)
	cid, _ := pausedForMismatch(t, k, ctx, mocks)

	require.ErrorIs(t, k.ResumeConsumerChain(ctx, cid), types.ErrInvalidConsumerClient)

	_, err := msgServer.UpdateConsumer(ctx, &types.MsgUpdateConsumer{Owner: k.GetAuthority(), ConsumerId: cid, NewChainId: chainIDTestNew})
	require.NoError(t, err)
	require.Equal(t, types.CONSUMER_PHASE_PAUSED, k.GetConsumerPhase(ctx, cid), "a rename does not resume")
	resumeMocks(mocks)
	require.NoError(t, k.ResumeConsumerChain(ctx, cid))
	require.Equal(t, types.CONSUMER_PHASE_LAUNCHED, k.GetConsumerPhase(ctx, cid))

	k.PauseConsumersWithMismatchedClients(ctx)
	require.Equal(t, types.CONSUMER_PHASE_LAUNCHED, k.GetConsumerPhase(ctx, cid), "client and consumer agree again")
}

// TestAMismatchPauseEndsWithARecoveryAndAResume: the second exit. Governance
// recovers the client back onto the consumer's chain id and resumes it; the
// consumer keeps its chain id and records no rename.
func TestAMismatchPauseEndsWithARecoveryAndAResume(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()
	cid, clientState := pausedForMismatch(t, k, ctx, mocks)

	clientState.ChainId = chainIDTestOld
	resumeMocks(mocks)
	require.NoError(t, k.ResumeConsumerChain(ctx, cid))
	require.Equal(t, types.CONSUMER_PHASE_LAUNCHED, k.GetConsumerPhase(ctx, cid))
	got, err := k.GetConsumerChainId(ctx, cid)
	require.NoError(t, err)
	require.Equal(t, chainIDTestOld, got)
	previous, err := k.GetConsumerPreviousChainIds(ctx, cid)
	require.NoError(t, err)
	require.Empty(t, previous)
}

// TestResumeRefusesAConsumerWhoseClientTracksAnotherChainId: the resume of a
// paused consumer waits for governance to rename it to its client's chain id.
func TestResumeRefusesAConsumerWhoseClientTracksAnotherChainId(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()
	cid := launchedWithRecoveredClient(t, k, ctx, mocks)
	require.NoError(t, k.PauseConsumerChain(ctx, cid, types.PAUSE_REASON_DOWNTIME_CHALLENGE))
	mocks.MockClientKeeper.EXPECT().GetClientStatus(gomock.Any(), chainIDTestClient).Return(ibcexported.Active).AnyTimes()

	err := k.ResumeConsumerChain(ctx, cid)
	require.ErrorIs(t, err, types.ErrInvalidConsumerClient)
	require.ErrorContains(t, err, "rename the consumer")
	require.Equal(t, types.CONSUMER_PHASE_PAUSED, k.GetConsumerPhase(ctx, cid))
}

// TestResumeRefusesAClientWhoseTrustingPeriodIsUnderTheHorizon: a recovery
// can put a shorter trusting period behind the declaration; the resume
// applies the declaration's floor again.
func TestResumeRefusesAClientWhoseTrustingPeriodIsUnderTheHorizon(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()
	cid := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, cid, types.CONSUMER_PHASE_LAUNCHED)
	k.SetConsumerChainId(ctx, cid, chainIDTestOld)
	k.SetConsumerClientId(ctx, cid, chainIDTestClient)
	k.SetInfractionParams(ctx, types.DefaultInfractionParameters())
	require.NoError(t, k.PauseConsumerChain(ctx, cid, types.PAUSE_REASON_DOWNTIME_CHALLENGE))
	mocks.MockClientKeeper.EXPECT().GetClientStatus(gomock.Any(), chainIDTestClient).Return(ibcexported.Active).AnyTimes()
	mocks.MockClientKeeper.EXPECT().GetClientState(gomock.Any(), chainIDTestClient).Return(&ibctmtypes.ClientState{
		ChainId: chainIDTestOld, TrustingPeriod: time.Hour,
	}, true).AnyTimes()

	err := k.ResumeConsumerChain(ctx, cid)
	require.ErrorContains(t, err, "trusting period")
	require.Equal(t, types.CONSUMER_PHASE_PAUSED, k.GetConsumerPhase(ctx, cid))
}

// TestGovernanceRenamesALaunchedConsumerToItsClientsChainId: the governance
// half of a rename follows the declared client and nothing else; the old id
// is kept with the client's height at the rename, the pending downtime
// accusations (which no header the recovered client verifies can disprove
// any more) are discarded, and the owner cannot do it.
func TestGovernanceRenamesALaunchedConsumerToItsClientsChainId(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()
	msgServer := providerkeeper.NewMsgServerImpl(&k)
	cid := launchedWithRecoveredClient(t, k, ctx, mocks)
	_, accused := newSweepValidator(t)
	putPendingDowntimeSlash(t, k, ctx, cid, accused, math.NewInt(100), ctx.BlockTime().Add(time.Hour), 100)
	k.MarkEpochDowntime(ctx, cid, accused.ToSdkConsAddr())

	_, err := msgServer.UpdateConsumer(ctx, &types.MsgUpdateConsumer{Owner: declOwner, ConsumerId: cid, NewChainId: chainIDTestNew})
	require.ErrorIs(t, err, types.ErrUnauthorized, "the owner cannot rename a launched consumer")

	_, err = msgServer.UpdateConsumer(ctx, &types.MsgUpdateConsumer{Owner: k.GetAuthority(), ConsumerId: cid, NewChainId: "consumer-3"})
	require.ErrorIs(t, err, types.ErrInvalidMsgUpdateConsumer, "the new id must be the client's")

	_, err = msgServer.UpdateConsumer(ctx, &types.MsgUpdateConsumer{Owner: k.GetAuthority(), ConsumerId: cid, NewChainId: chainIDTestNew})
	require.NoError(t, err)
	got, err := k.GetConsumerChainId(ctx, cid)
	require.NoError(t, err)
	require.Equal(t, chainIDTestNew, got)
	previous, err := k.GetConsumerPreviousChainIds(ctx, cid)
	require.NoError(t, err)
	require.Equal(t, []types.PreviousChainId{{ChainId: chainIDTestOld, Boundary: clienttypes.NewHeight(2, 500)}}, previous)
	require.Error(t, k.ValidateConsumerChainIdFree(ctx, chainIDTestOld), "the old id stays reserved as an evidence domain")
	has, err := k.PendingDowntimeSlashes.Has(ctx, collections.Join3(cid, accused.ToSdkConsAddr().Bytes(), int64(100)))
	require.NoError(t, err)
	require.False(t, has, "the pending downtime slash is discarded at the rename")
	require.False(t, k.IsEpochDowntime(ctx, cid, accused.ToSdkConsAddr()), "the epoch downtime mark is discarded at the rename")

	// Served again: the client and the consumer agree.
	k.AppendPendingVSCPackets(ctx, cid, vaastypes.ValidatorSetChangePacketData{ValsetUpdateId: 1})
	mocks.MockChannelV2Keeper.EXPECT().SendPacket(gomock.Any(), gomock.Any()).Return(&channeltypesv2.MsgSendPacketResponse{Sequence: 1}, nil).Times(1)
	require.NoError(t, k.SendVSCPackets(ctx))
	require.Equal(t, 0, countEvents(ctx, types.EventTypeConsumerClientChainIdMismatch))
}

// TestEvidenceChainIdForAcceptsAPreviousIdBelowTheBoundary: double-voting
// evidence from a block the client could have verified under the old id is
// still verified over it: any block of a lower revision, or of the boundary's
// revision at or below its height; above it, or for an id the consumer never
// had, it is refused.
func TestEvidenceChainIdForAcceptsAPreviousIdBelowTheBoundary(t *testing.T) {
	k, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()
	cid := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerChainId(ctx, cid, chainIDTestNew)
	// consumer-1 (revision 1) was renamed to consumer-2 at the client's height 500 of revision 2.
	require.NoError(t, k.ConsumerPreviousChainIds.Set(ctx, collections.Join(cid, chainIDTestOld), clienttypes.NewHeight(2, 500)))
	// And a rename within one revision: "shard" to "shard-renamed" at height 500.
	require.NoError(t, k.ConsumerPreviousChainIds.Set(ctx, collections.Join(cid, "shard"), clienttypes.NewHeight(0, 500)))

	got, err := k.EvidenceChainIdFor(ctx, cid, chainIDTestNew, 10_000)
	require.NoError(t, err)
	require.Equal(t, chainIDTestNew, got)
	got, err = k.EvidenceChainIdFor(ctx, cid, chainIDTestOld, 10_000)
	require.NoError(t, err, "a lower revision is below the boundary at any height")
	require.Equal(t, chainIDTestOld, got)
	got, err = k.EvidenceChainIdFor(ctx, cid, "shard", 500)
	require.NoError(t, err)
	require.Equal(t, "shard", got)
	_, err = k.EvidenceChainIdFor(ctx, cid, "shard", 501)
	require.ErrorIs(t, err, vaastypes.ErrInvalidDoubleVotingEvidence)
	_, err = k.EvidenceChainIdFor(ctx, cid, "consumer-9", 1)
	require.ErrorIs(t, err, vaastypes.ErrInvalidDoubleVotingEvidence)
}

// TestRenameRefusedAfterTheClientIsDeclaredPreLaunch: the declared client
// tracks a chain id, so a pre-launch rename comes before the declaration.
func TestRenameRefusedAfterTheClientIsDeclaredPreLaunch(t *testing.T) {
	k, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()
	msgServer := providerkeeper.NewMsgServerImpl(&k)
	cid := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, cid, types.CONSUMER_PHASE_INITIALIZED)
	k.SetConsumerChainId(ctx, cid, chainIDTestOld)
	k.SetConsumerOwnerAddress(ctx, cid, declOwner)
	k.SetConsumerClientId(ctx, cid, chainIDTestClient)

	_, err := msgServer.UpdateConsumer(ctx, &types.MsgUpdateConsumer{Owner: declOwner, ConsumerId: cid, NewChainId: chainIDTestNew})
	require.ErrorIs(t, err, types.ErrInvalidMsgUpdateConsumer)
	require.ErrorContains(t, err, "after its client")
}

// TestGenesisRoundTripKeepsPreviousChainIds: the renames survive a restart.
func TestGenesisRoundTripKeepsPreviousChainIds(t *testing.T) {
	pkA, ctxA, ctrlA, mocksA := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrlA.Finish()
	pkA.SetParams(ctxA, types.DefaultParams())
	pkA.SetValidatorSetUpdateId(ctxA, 1)
	cid := pkA.FetchAndIncrementConsumerId(ctxA)
	pkA.SetConsumerChainId(ctxA, cid, chainIDTestNew)
	pkA.SetConsumerPhase(ctxA, cid, types.CONSUMER_PHASE_LAUNCHED)
	pkA.SetConsumerOwnerAddress(ctxA, cid, declOwner)
	require.NoError(t, pkA.SetConsumerMetadata(ctxA, cid, types.ConsumerMetadata{Name: "n", Description: "d", Metadata: "m"}))
	require.NoError(t, pkA.SetConsumerInitializationParameters(ctxA, cid, types.ConsumerInitializationParameters{
		InitialHeight: clienttypes.Height{RevisionNumber: 1, RevisionHeight: 42}, GenesisHash: []byte("g"), BinaryHash: []byte("b"),
		SpawnTime: time.Unix(1_700_000_000, 0).UTC(), UnbondingPeriod: time.Hour, VaasTimeoutPeriod: time.Hour, HistoricalEntries: 10,
	}))
	pkA.SetConsumerClientId(ctxA, cid, chainIDTestClient)
	require.NoError(t, pkA.SetConsumerGenesis(ctxA, cid, launchedConsumerGenesis()))
	pkA.SetEquivocationEvidenceMinHeight(ctxA, cid, 42)
	require.NoError(t, pkA.ConsumerPreviousChainIds.Set(ctxA, collections.Join(cid, chainIDTestOld), clienttypes.NewHeight(2, 500)))

	expA := pkA.ExportGenesis(ctxA)
	require.NoError(t, expA.Validate())
	require.Equal(t, []types.PreviousChainId{{ChainId: chainIDTestOld, Boundary: clienttypes.NewHeight(2, 500)}}, expA.ConsumerStates[0].PreviousChainIds)

	pkB, ctxB, ctrlB, mocksB := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrlB.Finish()
	mocksA.MockStakingKeeper.EXPECT().GetBondedValidatorsByPower(gomock.Any()).Return(nil, nil).AnyTimes()
	mocksB.MockStakingKeeper.EXPECT().GetBondedValidatorsByPower(gomock.Any()).Return(nil, nil).AnyTimes()
	mocksB.MockBankKeeper.EXPECT().GetAllBalances(gomock.Any(), gomock.Any()).Return(sdk.NewCoins()).AnyTimes()
	_ = pkB.InitGenesis(ctxB, expA)
	previous, err := pkB.GetConsumerPreviousChainIds(ctxB, cid)
	require.NoError(t, err)
	require.Equal(t, []types.PreviousChainId{{ChainId: chainIDTestOld, Boundary: clienttypes.NewHeight(2, 500)}}, previous)

	// No other consumer may hold a previous chain id.
	taken := expA.ConsumerStates[0]
	taken.ConsumerId = 7
	taken.ChainId = chainIDTestOld
	taken.PreviousChainIds = nil
	withTaken := *expA
	withTaken.ConsumerStates = append([]types.ConsumerState{expA.ConsumerStates[0]}, taken)
	require.ErrorContains(t, withTaken.Validate(), "previous chain id")

	// A rename belongs to a launched consumer.
	expA.ConsumerStates[0].Phase = types.CONSUMER_PHASE_REGISTERED
	expA.ConsumerStates[0].ClientId = ""
	require.ErrorContains(t, expA.Validate(), "only set after launch")
}

// TestGovernanceRenameRefusesAClientUnderTheTrustingPeriodFloor: the client
// a rename follows is served from the rename on, resumed or not, so it meets
// the declaration's trusting-period floor.
func TestGovernanceRenameRefusesAClientUnderTheTrustingPeriodFloor(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()
	msgServer := providerkeeper.NewMsgServerImpl(&k)
	cid, clientState := pausedForMismatch(t, k, ctx, mocks)
	clientState.TrustingPeriod = time.Hour

	_, err := msgServer.UpdateConsumer(ctx, &types.MsgUpdateConsumer{Owner: k.GetAuthority(), ConsumerId: cid, NewChainId: chainIDTestNew})
	require.ErrorIs(t, err, types.ErrInvalidMsgUpdateConsumer)
	require.ErrorContains(t, err, "trusting period")
	got, err := k.GetConsumerChainId(ctx, cid)
	require.NoError(t, err)
	require.Equal(t, chainIDTestOld, got)
}

// TestGovernanceRenameBackToAPreviousChainIdIsRefused: a chain id the
// consumer carried before a rename stays its evidence domain, so renaming
// back to it (A to B to A) is refused like any taken chain id.
func TestGovernanceRenameBackToAPreviousChainIdIsRefused(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()
	msgServer := providerkeeper.NewMsgServerImpl(&k)
	cid, clientState := pausedForMismatch(t, k, ctx, mocks)
	_, err := msgServer.UpdateConsumer(ctx, &types.MsgUpdateConsumer{Owner: k.GetAuthority(), ConsumerId: cid, NewChainId: chainIDTestNew})
	require.NoError(t, err)

	clientState.ChainId = chainIDTestOld
	_, err = msgServer.UpdateConsumer(ctx, &types.MsgUpdateConsumer{Owner: k.GetAuthority(), ConsumerId: cid, NewChainId: chainIDTestOld})
	require.ErrorIs(t, err, types.ErrDuplicateChainId)
	got, err := k.GetConsumerChainId(ctx, cid)
	require.NoError(t, err)
	require.Equal(t, chainIDTestNew, got)
}

// TestGovernanceRenamesAStoppedConsumer: a mismatch pause that outlives
// MaxPauseDuration stops the consumer with its record still naming the old
// chain id. The governance rename is accepted on the stopped consumer under
// the same checks, so the old id is kept with its boundary and the client's
// chain id is on record to verify double-voting evidence over until the
// deletion; the owner cannot update a stopped consumer, and a deleted one
// admits nothing.
func TestGovernanceRenamesAStoppedConsumer(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()
	msgServer := providerkeeper.NewMsgServerImpl(&k)
	k.SetParams(ctx, types.DefaultParams())
	ctx = ctx.WithBlockTime(time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC))
	cid, _ := pausedForMismatch(t, k, ctx, mocks)
	mocks.MockStakingKeeper.EXPECT().UnbondingTime(gomock.Any()).Return(21*24*time.Hour, nil).AnyTimes()
	ctx = ctx.WithBlockTime(ctx.BlockTime().Add(k.GetMaxPauseDuration(ctx)))
	require.NoError(t, k.BeginBlockAutoStopPausedConsumers(ctx))
	require.Equal(t, types.CONSUMER_PHASE_STOPPED, k.GetConsumerPhase(ctx, cid))

	_, err := msgServer.UpdateConsumer(ctx, &types.MsgUpdateConsumer{Owner: declOwner, ConsumerId: cid, NewChainId: chainIDTestNew})
	require.ErrorIs(t, err, types.ErrUnauthorized, "the owner cannot update a stopped consumer")
	got, err := k.GetConsumerChainId(ctx, cid)
	require.NoError(t, err)
	require.Equal(t, chainIDTestOld, got)

	_, err = msgServer.UpdateConsumer(ctx, &types.MsgUpdateConsumer{Owner: k.GetAuthority(), ConsumerId: cid, NewChainId: chainIDTestNew})
	require.NoError(t, err)
	require.Equal(t, types.CONSUMER_PHASE_STOPPED, k.GetConsumerPhase(ctx, cid), "a rename does not revive a stopped consumer")
	got, err = k.GetConsumerChainId(ctx, cid)
	require.NoError(t, err)
	require.Equal(t, chainIDTestNew, got)
	previous, err := k.GetConsumerPreviousChainIds(ctx, cid)
	require.NoError(t, err)
	require.Equal(t, []types.PreviousChainId{{ChainId: chainIDTestOld, Boundary: clienttypes.NewHeight(2, 500)}}, previous)
	evidenceChainId, err := k.EvidenceChainIdFor(ctx, cid, chainIDTestNew, 600)
	require.NoError(t, err, "votes signed over the client's chain id are verified over it from the rename on")
	require.Equal(t, chainIDTestNew, evidenceChainId)

	k.SetConsumerPhase(ctx, cid, types.CONSUMER_PHASE_DELETED)
	_, err = msgServer.UpdateConsumer(ctx, &types.MsgUpdateConsumer{Owner: k.GetAuthority(), ConsumerId: cid, NewChainId: "consumer-3"})
	require.ErrorIs(t, err, types.ErrInvalidPhase, "a deleted consumer admits no update")
}
