package keeper_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"

	tmproto "github.com/cometbft/cometbft/proto/tendermint/types"
	tmtypes "github.com/cometbft/cometbft/types"

	channeltypesv2 "github.com/cosmos/ibc-go/v10/modules/core/04-channel/v2/types"
	ibcexported "github.com/cosmos/ibc-go/v10/modules/core/exported"
	ibctmtypes "github.com/cosmos/ibc-go/v10/modules/light-clients/07-tendermint"

	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	cryptotestutil "github.com/allinbits/vaas/testutil/crypto"
	testkeeper "github.com/allinbits/vaas/testutil/keeper"
	"github.com/allinbits/vaas/x/vaas/provider/types"
	vaastypes "github.com/allinbits/vaas/x/vaas/types"
)

const reaccuseTestChainID = "consumer-chain-reaccuse"

// TestResumeThenReaccuse_SameWindowRejectedLaterWindowChallengeable covers
// the pause/resume survivability of the acceptance bookkeeping end to end:
// AcceptedDowntimeWindows records are written at evidence acceptance
// (HandleConsumerDowntime) and deliberately survive PauseConsumerChain's
// CancelConsumerDowntimeState, which clears only PendingDowntimeSlashes and
// EpochDowntime, and a subsequent resume. So:
//
//  1. downtime evidence for the window a successful challenge just disproved
//     cannot be resubmitted after the consumer resumes, even though nothing
//     is pending for it anymore -- its accepted record survives;
//  2. evidence for a later, disjoint window is still accepted after resume,
//     and is itself challengeable -- proven here with a second genuine
//     challenge, not just acceptance into PendingDowntimeSlashes.
func TestResumeThenReaccuse_SameWindowRejectedLaterWindowChallengeable(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	const cid = uint64(0)
	const clientId = "07-tendermint-0"
	windowEndTime := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	ctx = ctx.WithBlockTime(windowEndTime)

	k.SetConsumerPhase(ctx, cid, types.CONSUMER_PHASE_LAUNCHED)
	k.SetConsumerChainId(ctx, cid, reaccuseTestChainID)
	k.SetConsumerClientId(ctx, cid, clientId)
	k.SetEquivocationEvidenceMinHeight(ctx, cid, 1)
	require.NoError(t, k.SetConsumerInitializationParameters(ctx, cid, types.ConsumerInitializationParameters{
		SpawnTime: windowEndTime.Add(-30 * 24 * time.Hour),
	}))
	infractionParams := downtimeParams(8, "0.5", 0, 7*24*time.Hour, 72*time.Hour)
	k.SetInfractionParams(ctx, infractionParams)
	k.OverrideWindowEndTimestampForTest(func(sdk.Context, string, int64) (time.Time, error) {
		return windowEndTime, nil
	})
	k.OverrideVerifyDowntimeChallengeHeaderForTest(func(sdk.Context, string, *ibctmtypes.Header) error {
		return nil
	})

	signer := tmtypes.NewMockPV()
	pubKey, err := signer.GetPubKey()
	require.NoError(t, err)
	addr := pubKey.Address()
	providerAddr := types.NewProviderConsAddress(sdk.ConsAddress(addr))
	consAddr := providerAddr.ToSdkConsAddr()

	cosmosPubKey, err := cryptocodec.FromCmtPubKeyInterface(pubKey)
	require.NoError(t, err)
	validator, err := stakingtypes.NewValidator(
		sdk.ValAddress(cosmosPubKey.Address()).String(), cosmosPubKey, stakingtypes.NewDescription("", "", "", "", ""),
	)
	require.NoError(t, err)
	validator.Status = stakingtypes.Bonded
	validator.Tokens = sdk.DefaultPowerReduction
	validator.DelegatorShares = math.LegacyNewDecFromInt(sdk.DefaultPowerReduction)
	cmtPubKey, err := validator.CmtConsPublicKey()
	require.NoError(t, err)
	require.NoError(t, k.SetConsumerValidator(ctx, cid, types.ConsensusValidator{
		ProviderConsAddr: consAddr,
		Power:            1000,
		PublicKey:        &cmtPubKey,
		JoinHeight:       1,
	}))

	k.SetEpochShareRecord(ctx, cid, windowEndTime, math.NewInt(1000))
	mocks.MockPhotonKeeper.EXPECT().ConversionRate(gomock.Any()).Return(math.LegacyNewDec(2), nil).AnyTimes()

	firstWindowKey := collections.Join3(cid, consAddr.Bytes(), int64(100))

	// --- accept evidence for window [93, 100] (span 8), 6 of 8 missed ---
	firstEvidence := vaastypes.NewEvidencePacketData(sdk.ConsAddress(consAddr), 93, []byte{0x3F}, 8, 8, math.LegacyMustNewDecFromStr("0.5"))
	require.NoError(t, k.HandleConsumerEvidencePacket(ctx, cid, firstEvidence))

	pending, err := k.PendingDowntimeSlashes.Get(ctx, firstWindowKey)
	require.NoError(t, err)
	require.Equal(t, int64(93), pending.WindowStartHeight)

	firstAccepted, err := k.AcceptedDowntimeWindows.Get(ctx, firstWindowKey)
	require.NoError(t, err)
	require.Equal(t, int64(93), firstAccepted.WindowStart)

	// challenge disproves claimedHeight within [windowStart, windowStart+8):
	// bit (claimedHeight-windowStart) must be set in the 0x3F bitmap, i.e.
	// claimedHeight in [windowStart, windowStart+6). No withheld fee record is
	// ever seeded in this test, so PayWithheldFees has nothing to iterate and
	// needs no bank/staking mocks.
	challenge := func(claimedHeight int64) error {
		t.Helper()
		blockID := cryptotestutil.MakeBlockID([]byte("blockhash"), 1, []byte("partshash"))
		vote, err := tmtypes.MakeVote(signer, reaccuseTestChainID, 0, claimedHeight, 0, tmproto.PrecommitType, blockID, ctx.BlockTime())
		require.NoError(t, err)
		commit := &tmtypes.Commit{
			Height:     claimedHeight,
			Round:      0,
			BlockID:    blockID,
			Signatures: []tmtypes.CommitSig{vote.CommitSig()},
		}
		header := &ibctmtypes.Header{
			SignedHeader: &tmproto.SignedHeader{
				Header: &tmproto.Header{
					ChainID:        reaccuseTestChainID,
					Height:         claimedHeight + 1,
					LastCommitHash: commit.Hash(),
				},
				Commit: &tmproto.Commit{},
			},
		}
		msg := &types.MsgChallengeConsumerDowntime{
			Signer:          "cosmos1qypqxpq9qcrsszgse4wwrq4vt3s2r0y8ryqhx7",
			ConsumerId:      cid,
			ValidatorAddr:   addr,
			ClaimedHeight:   claimedHeight,
			Header:          header,
			LastCommit:      commit.ToProto(),
			ValidatorPubkey: pubKey.Bytes(),
		}
		return k.HandleChallengeConsumerDowntime(ctx, msg)
	}

	require.NoError(t, challenge(95))
	require.Equal(t, types.CONSUMER_PHASE_PAUSED, k.GetConsumerPhase(ctx, cid))
	_, err = k.PendingDowntimeSlashes.Get(ctx, firstWindowKey)
	require.ErrorIs(t, err, collections.ErrNotFound)
	// The accepted-window record survives the pause -- only
	// DeleteConsumerChain erases it.
	firstAccepted, err = k.AcceptedDowntimeWindows.Get(ctx, firstWindowKey)
	require.NoError(t, err)
	require.Equal(t, int64(93), firstAccepted.WindowStart)

	// --- resume ---
	// The forced resume snapshot recomputes the consumer's validator set from
	// the currently bonded set (ComputeConsumerNextValSet), so the accused
	// validator must still be reported as bonded here or it would silently
	// drop out of GetConsumerValidator and short-circuit the post-resume
	// evidence checks below before they ever reach the window-overlap logic
	// this test is actually about.
	mocks.MockClientKeeper.EXPECT().GetClientStatus(gomock.Any(), clientId).Return(ibcexported.Active)
	mocks.MockStakingKeeper.EXPECT().MaxValidators(gomock.Any()).Return(uint32(100), nil).AnyTimes()
	mocks.MockStakingKeeper.EXPECT().GetBondedValidatorsByPower(gomock.Any()).Return([]stakingtypes.Validator{validator}, nil).AnyTimes()
	mocks.MockStakingKeeper.EXPECT().GetLastValidatorPower(gomock.Any(), gomock.Any()).Return(int64(1000), nil).AnyTimes()
	mocks.MockChannelV2Keeper.EXPECT().SendPacket(gomock.Any(), gomock.Any()).
		Return(&channeltypesv2.MsgSendPacketResponse{Sequence: 1}, nil).Times(1)

	require.NoError(t, k.ResumeConsumerChain(ctx, cid))
	require.Equal(t, types.CONSUMER_PHASE_LAUNCHED, k.GetConsumerPhase(ctx, cid))

	// (1) the disproven window, and anything intersecting it, is rejected
	// post-resume even though PendingDowntimeSlashes for it is long gone.
	duplicate := vaastypes.NewEvidencePacketData(sdk.ConsAddress(consAddr), 93, []byte{0x3F}, 8, 8, math.LegacyMustNewDecFromStr("0.5"))
	err = k.HandleConsumerEvidencePacket(ctx, cid, duplicate)
	require.Error(t, err)
	require.Contains(t, err.Error(), "already accepted")

	overlapping := vaastypes.NewEvidencePacketData(sdk.ConsAddress(consAddr), 95, []byte{0x3F}, 8, 8, math.LegacyMustNewDecFromStr("0.5"))
	err = k.HandleConsumerEvidencePacket(ctx, cid, overlapping)
	require.Error(t, err)
	require.Contains(t, err.Error(), "already accepted")

	// (2) a later, disjoint window (start 101 > 100) is accepted...
	laterEvidence := vaastypes.NewEvidencePacketData(sdk.ConsAddress(consAddr), 101, []byte{0x3F}, 8, 8, math.LegacyMustNewDecFromStr("0.5"))
	require.NoError(t, k.HandleConsumerEvidencePacket(ctx, cid, laterEvidence))

	// windowEnd = windowStart(101) + span(8) - 1 = 108.
	secondWindowKey := collections.Join3(cid, consAddr.Bytes(), int64(108))
	laterAccepted, err := k.AcceptedDowntimeWindows.Get(ctx, secondWindowKey)
	require.NoError(t, err)
	require.Equal(t, int64(101), laterAccepted.WindowStart)

	// ...and is itself challengeable: a second genuine challenge against the
	// new window (claimed height 103, same bit-2-of-0x3F offset as before)
	// succeeds too.
	require.NoError(t, challenge(103))
	require.Equal(t, types.CONSUMER_PHASE_PAUSED, k.GetConsumerPhase(ctx, cid))
	_, err = k.PendingDowntimeSlashes.Get(ctx, secondWindowKey)
	require.ErrorIs(t, err, collections.ErrNotFound)
}
