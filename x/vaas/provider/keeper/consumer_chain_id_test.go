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

// launchedWithClient seeds a launched consumer with a chain id and a declared
// client, and makes the client report the given chain id at the given height.
func launchedWithClient(t *testing.T, k providerkeeper.Keeper, ctx sdk.Context, mocks testkeeper.MockedKeepers, clientChainID string, height uint64) uint64 {
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
	mocks.MockClientKeeper.EXPECT().GetClientState(gomock.Any(), chainIDTestClient).Return(&ibctmtypes.ClientState{
		ChainId:        clientChainID,
		TrustingPeriod: 14 * 24 * time.Hour,
		LatestHeight:   clienttypes.NewHeight(clienttypes.ParseChainID(clientChainID), height),
	}, true).AnyTimes()
	return cid
}

// TestSendVSCPacketsSkipsAConsumerWhoseClientTracksAnotherChainId: a client
// recovered onto a renamed chain is not served. The packet stays queued, no
// send is attempted (gomock would fail on one) and the mismatch is an event.
func TestSendVSCPacketsSkipsAConsumerWhoseClientTracksAnotherChainId(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()
	cid := launchedWithClient(t, k, ctx, mocks, chainIDTestNew, 500)
	k.AppendPendingVSCPackets(ctx, cid, vaastypes.ValidatorSetChangePacketData{
		ValidatorUpdates: []abci.ValidatorUpdate{}, ValsetUpdateId: 1,
	})

	require.NoError(t, k.SendVSCPackets(ctx))

	require.Len(t, k.GetPendingVSCPackets(ctx, cid), 1, "the packet stays queued")
	require.Equal(t, 1, countEvents(ctx, types.EventTypeConsumerClientChainIdMismatch))
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

// TestSweepsWaitOnAConsumerWhoseClientTracksAnotherChainId: a pending slash
// and a withheld fee record that mature during the mismatch (the recovery
// landed, the rename has not) wait for the rename, which discards and repays
// them; neither executes nor expires meanwhile.
func TestSweepsWaitOnAConsumerWhoseClientTracksAnotherChainId(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()
	ctx = ctx.WithBlockTime(time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC))
	cid := launchedWithClient(t, k, ctx, mocks, chainIDTestNew, 500)
	_, accused := newSweepValidator(t)
	matured := ctx.BlockTime().Add(-time.Minute)
	putPendingDowntimeSlash(t, k, ctx, cid, accused, math.NewInt(100), matured, 100)
	recordKey := collections.Join(cid, accused.ToSdkConsAddr().Bytes())
	require.NoError(t, k.WithheldFeeRecords.Set(ctx, recordKey, types.WithheldFeeRecord{
		ConsumerId: cid, ProviderConsAddr: accused.ToSdkConsAddr().Bytes(), Amount: sdk.NewInt64Coin("uphoton", 5), ExpiresAt: matured,
	}))

	// No staking expectations: an execution would fail on the mock.
	k.SweepPendingDowntimeSlashes(ctx)
	k.SweepExpiredWithheldFeeRecords(ctx)

	pending, err := k.PendingDowntimeSlashes.Get(ctx, collections.Join3(cid, accused.ToSdkConsAddr().Bytes(), int64(100)))
	require.NoError(t, err, "the matured slash waits")
	require.Equal(t, matured, pending.MaturesAt)
	has, err := k.WithheldFeeRecords.Has(ctx, recordKey)
	require.NoError(t, err)
	require.True(t, has, "the expired withheld fee record waits")
}

// TestResumeRefusesAConsumerWhoseClientTracksAnotherChainId: the resume of a
// paused consumer waits for governance to rename it to its client's chain id.
func TestResumeRefusesAConsumerWhoseClientTracksAnotherChainId(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()
	cid := launchedWithClient(t, k, ctx, mocks, chainIDTestNew, 500)
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
	cid := launchedWithClient(t, k, ctx, mocks, chainIDTestNew, 500)
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
