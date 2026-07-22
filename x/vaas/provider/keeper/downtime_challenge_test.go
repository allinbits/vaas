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

	ibctmtypes "github.com/cosmos/ibc-go/v10/modules/light-clients/07-tendermint"

	"github.com/cosmos/cosmos-sdk/codec/address"
	sdk "github.com/cosmos/cosmos-sdk/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"

	cryptotestutil "github.com/allinbits/vaas/testutil/crypto"
	testkeeper "github.com/allinbits/vaas/testutil/keeper"
	providerkeeper "github.com/allinbits/vaas/x/vaas/provider/keeper"
	"github.com/allinbits/vaas/x/vaas/provider/types"
	vaastypes "github.com/allinbits/vaas/x/vaas/types"
)

const challengeTestChainID = "consumer-chain-0"

// challengeFixture bundles a fully valid MsgChallengeConsumerDowntime (and
// the provider-side state it disproves) so each test case can start from a
// known-good baseline and corrupt exactly one thing. The commit and header
// are genuine cometbft objects: commit is signed by a real ed25519 key and
// header.Header.LastCommitHash is set to commit.Hash(), exactly as an honest
// challenger would submit.
//
// The light-client verification step is bypassed via
// OverrideVerifyDowntimeChallengeHeaderForTest: fabricating a real IBC client
// store (consensus states, trusted validator sets) is impractical in a
// keeper unit test, so this single step is stubbed behind the same seam
// style as windowEndTimestamp. Every other verification step (1, 2, 4, 5)
// runs against real data.
type challengeFixture struct {
	k               providerkeeper.Keeper
	ctx             sdk.Context
	mocks           testkeeper.MockedKeepers
	cid             uint64
	providerAddr    types.ProviderConsAddress
	claimedHeight   int64
	windowEndHeight int64
	msg             *types.MsgChallengeConsumerDowntime
}

func newChallengeFixture(t *testing.T) *challengeFixture {
	t.Helper()

	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	t.Cleanup(ctrl.Finish)

	k.OverrideVerifyDowntimeChallengeHeaderForTest(func(sdk.Context, string, *ibctmtypes.Header) error {
		return nil
	})

	cid := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, cid, types.CONSUMER_PHASE_LAUNCHED)
	k.SetConsumerChainId(ctx, cid, challengeTestChainID)
	k.SetConsumerClientId(ctx, cid, "07-tendermint-0")

	signer := tmtypes.NewMockPV()
	pubKey, err := signer.GetPubKey()
	require.NoError(t, err)
	addr := pubKey.Address()
	providerAddr := types.NewProviderConsAddress(sdk.ConsAddress(addr))

	const (
		claimedHeight = int64(100)
		windowStart   = claimedHeight
		span          = int64(5)
	)
	pending := types.PendingDowntimeSlash{
		ConsumerId:         cid,
		ProviderConsAddr:   providerAddr.ToSdkConsAddr().Bytes(),
		WindowStartHeight:  windowStart,
		Span:               span,
		MissedCount:        1,
		MissedBlocksBitmap: []byte{0x01}, // bit 0 (claimedHeight - windowStart) marked missed
		SlashTokens:        math.NewInt(1000),
		MaturesAt:          ctx.BlockTime().Add(time.Hour),
	}
	windowEndHeight := windowStart + span - 1
	require.NoError(t, k.PendingDowntimeSlashes.Set(ctx, collections.Join3(cid, providerAddr.ToSdkConsAddr().Bytes(), windowEndHeight), pending))

	amount := sdk.NewInt64Coin("uphoton", 500)
	require.NoError(t, k.WithheldFeeRecords.Set(ctx, collections.Join(cid, providerAddr.ToSdkConsAddr().Bytes()), types.WithheldFeeRecord{
		ConsumerId:       cid,
		ProviderConsAddr: providerAddr.ToSdkConsAddr().Bytes(),
		Amount:           amount,
		ExpiresAt:        ctx.BlockTime().Add(time.Hour),
	}))

	blockID := cryptotestutil.MakeBlockID([]byte("blockhash"), 1, []byte("partshash"))
	vote, err := tmtypes.MakeVote(signer, challengeTestChainID, 0, claimedHeight, 0, tmproto.PrecommitType, blockID, ctx.BlockTime())
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
				ChainID:        challengeTestChainID,
				Height:         claimedHeight + 1,
				LastCommitHash: commit.Hash(),
			},
			// The header's own Commit (sealing H+1, not H) is irrelevant here:
			// light-client verification of it is stubbed above, and
			// HandleChallengeConsumerDowntime never reads it.
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

	return &challengeFixture{
		k:               k,
		ctx:             ctx,
		mocks:           mocks,
		cid:             cid,
		providerAddr:    providerAddr,
		claimedHeight:   claimedHeight,
		windowEndHeight: windowEndHeight,
		msg:             msg,
	}
}

// pendingSlashKey returns the PendingDowntimeSlashes key (consumer,
// validator, window-end height) for the fixture's single seeded window.
func (f *challengeFixture) pendingSlashKey() collections.Triple[uint64, []byte, int64] {
	return collections.Join3(f.cid, f.providerAddr.ToSdkConsAddr().Bytes(), f.windowEndHeight)
}

// pairKey returns the (consumer, validator) key used by WithheldFeeRecords
// and DowntimeWindowFloors, which are keyed per pair, not per window.
func (f *challengeFixture) pairKey() collections.Pair[uint64, []byte] {
	return collections.Join(f.cid, f.providerAddr.ToSdkConsAddr().Bytes())
}

// requireNoStateChange asserts the invariant every failure path must
// preserve: the pending slash and withheld fee record survive untouched and
// the consumer never leaves LAUNCHED.
func (f *challengeFixture) requireNoStateChange(t *testing.T) {
	t.Helper()

	_, err := f.k.PendingDowntimeSlashes.Get(f.ctx, f.pendingSlashKey())
	require.NoError(t, err, "pending downtime slash must survive a failed challenge")

	require.Equal(t, types.CONSUMER_PHASE_LAUNCHED, f.k.GetConsumerPhase(f.ctx, f.cid))

	has, err := f.k.WithheldFeeRecords.Has(f.ctx, f.pairKey())
	require.NoError(t, err)
	require.True(t, has, "withheld fee record must survive a failed challenge")
}

// TestHandleChallengeConsumerDowntime_Success verifies the full happy path:
// a genuine signature disproving the claimed-missed height cancels the
// pending slash, retro-pays the withheld fee record, pauses the consumer,
// and emits both the challenge-succeeded and consumer-paused events.
func TestHandleChallengeConsumerDowntime_Success(t *testing.T) {
	f := newChallengeFixture(t)

	valAddrCodec := address.NewBech32Codec("cosmosvaloper")
	f.mocks.MockStakingKeeper.EXPECT().ValidatorAddressCodec().Return(valAddrCodec).AnyTimes()

	val, valBytes := newBondedValidator(t, valAddrCodec, 7)
	f.mocks.MockStakingKeeper.EXPECT().
		GetValidatorByConsAddr(gomock.Any(), f.providerAddr.ToSdkConsAddr()).
		Return(val, nil)

	poolAddr := f.k.GetConsumerFeePoolAddress(f.cid)
	amount := sdk.NewInt64Coin("uphoton", 500)
	f.mocks.MockBankKeeper.EXPECT().
		GetBalance(gomock.Any(), poolAddr, "uphoton").
		Return(sdk.NewInt64Coin("uphoton", 1000))
	f.mocks.MockBankKeeper.EXPECT().
		InputOutputCoins(gomock.Any(),
			banktypes.Input{Address: poolAddr.String(), Coins: sdk.NewCoins(amount)},
			[]banktypes.Output{{Address: accAddr(valBytes), Coins: sdk.NewCoins(amount)}},
		).Return(nil)

	require.NoError(t, f.k.HandleChallengeConsumerDowntime(f.ctx, f.msg))

	_, err := f.k.PendingDowntimeSlashes.Get(f.ctx, f.pendingSlashKey())
	require.ErrorIs(t, err, collections.ErrNotFound, "pending slash must be cancelled")

	require.Equal(t, types.CONSUMER_PHASE_PAUSED, f.k.GetConsumerPhase(f.ctx, f.cid))

	has, err := f.k.WithheldFeeRecords.Has(f.ctx, f.pairKey())
	require.NoError(t, err)
	require.False(t, has, "withheld fee record must be paid out and deleted")

	var sawChallenge, sawPause bool
	for _, ev := range f.ctx.EventManager().Events() {
		switch ev.Type {
		case vaastypes.EventTypeDowntimeChallengeSucceeded:
			sawChallenge = true
			attrs := map[string]string{}
			for _, a := range ev.Attributes {
				attrs[a.Key] = a.Value
			}
			require.Equal(t, f.msg.Signer, attrs[vaastypes.AttributeChallenger])
			require.Equal(t, f.providerAddr.String(), attrs[vaastypes.AttributeProviderValidatorAddress])
			require.Equal(t, "100", attrs[vaastypes.AttributeClaimedHeight])
		case vaastypes.EventTypeConsumerPaused:
			sawPause = true
		}
	}
	require.True(t, sawChallenge, "challenge-succeeded event not emitted")
	require.True(t, sawPause, "consumer-paused event not emitted")
}

// TestHandleChallengeConsumerDowntime_FindsContainingWindowAmongSeveral
// verifies step 1's lookup: with two disjoint windows pending for the
// same (consumer, validator) pair, a challenge naming a height in the
// *second* window is matched against that window (not the first, and not
// rejected as "no pending slash"), and succeeds -- cancelling every pending
// slash for the consumer, including the untouched first window.
func TestHandleChallengeConsumerDowntime_FindsContainingWindowAmongSeveral(t *testing.T) {
	k, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	k.OverrideVerifyDowntimeChallengeHeaderForTest(func(sdk.Context, string, *ibctmtypes.Header) error {
		return nil
	})

	cid := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, cid, types.CONSUMER_PHASE_LAUNCHED)
	k.SetConsumerChainId(ctx, cid, challengeTestChainID)
	k.SetConsumerClientId(ctx, cid, "07-tendermint-0")

	signer := tmtypes.NewMockPV()
	pubKey, err := signer.GetPubKey()
	require.NoError(t, err)
	addr := pubKey.Address()
	providerAddr := types.NewProviderConsAddress(sdk.ConsAddress(addr))

	// window 1: [100, 105), bit 0 (height 100) marked missed.
	window1 := types.PendingDowntimeSlash{
		ConsumerId:         cid,
		ProviderConsAddr:   providerAddr.ToSdkConsAddr().Bytes(),
		WindowStartHeight:  100,
		Span:               5,
		MissedCount:        1,
		MissedBlocksBitmap: []byte{0x01},
		SlashTokens:        math.NewInt(1000),
		MaturesAt:          ctx.BlockTime().Add(time.Hour),
	}
	require.NoError(t, k.PendingDowntimeSlashes.Set(ctx, collections.Join3(cid, providerAddr.ToSdkConsAddr().Bytes(), int64(104)), window1))

	// window 2: [110, 115), bit 2 (height 112) marked missed -- disjoint from
	// window 1.
	window2 := types.PendingDowntimeSlash{
		ConsumerId:         cid,
		ProviderConsAddr:   providerAddr.ToSdkConsAddr().Bytes(),
		WindowStartHeight:  110,
		Span:               5,
		MissedCount:        1,
		MissedBlocksBitmap: []byte{0x04},
		SlashTokens:        math.NewInt(1000),
		MaturesAt:          ctx.BlockTime().Add(time.Hour),
	}
	require.NoError(t, k.PendingDowntimeSlashes.Set(ctx, collections.Join3(cid, providerAddr.ToSdkConsAddr().Bytes(), int64(114)), window2))

	const claimedHeight = int64(112)
	blockID := cryptotestutil.MakeBlockID([]byte("blockhash"), 1, []byte("partshash"))
	vote, err := tmtypes.MakeVote(signer, challengeTestChainID, 0, claimedHeight, 0, tmproto.PrecommitType, blockID, ctx.BlockTime())
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
				ChainID:        challengeTestChainID,
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

	// No withheld fee record is seeded, so PayWithheldFees has nothing to
	// iterate and this test needs no bank/staking mocks.
	require.NoError(t, k.HandleChallengeConsumerDowntime(ctx, msg))

	require.Equal(t, types.CONSUMER_PHASE_PAUSED, k.GetConsumerPhase(ctx, cid))
	_, err = k.PendingDowntimeSlashes.Get(ctx, collections.Join3(cid, providerAddr.ToSdkConsAddr().Bytes(), int64(104)))
	require.ErrorIs(t, err, collections.ErrNotFound, "the untouched first window must also be cancelled by the success path")
	_, err = k.PendingDowntimeSlashes.Get(ctx, collections.Join3(cid, providerAddr.ToSdkConsAddr().Bytes(), int64(114)))
	require.ErrorIs(t, err, collections.ErrNotFound)
}

// TestHandleChallengeConsumerDowntime_SuccessAtLastWindowHeight exercises the
// window-containment upper bound and the byte/bit indexing together: the
// claimed height is the LAST height of the pending window, and the window's
// span (12) pushes that height's missed bit into the SECOND bitmap byte
// (bit 11). The challenge succeeds down the full path: the pending slash is
// cancelled and the consumer is paused.
func TestHandleChallengeConsumerDowntime_SuccessAtLastWindowHeight(t *testing.T) {
	k, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	k.OverrideVerifyDowntimeChallengeHeaderForTest(func(sdk.Context, string, *ibctmtypes.Header) error {
		return nil
	})

	cid := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, cid, types.CONSUMER_PHASE_LAUNCHED)
	k.SetConsumerChainId(ctx, cid, challengeTestChainID)
	k.SetConsumerClientId(ctx, cid, "07-tendermint-0")

	signer := tmtypes.NewMockPV()
	pubKey, err := signer.GetPubKey()
	require.NoError(t, err)
	addr := pubKey.Address()
	providerAddr := types.NewProviderConsAddress(sdk.ConsAddress(addr))

	// window [100, 111]: span 12, claimed height 111 is the last in-window
	// height and maps to bit 11 -- bit 3 of the second bitmap byte.
	const (
		windowStart   = int64(100)
		span          = int64(12)
		claimedHeight = windowStart + span - 1
	)
	pending := types.PendingDowntimeSlash{
		ConsumerId:         cid,
		ProviderConsAddr:   providerAddr.ToSdkConsAddr().Bytes(),
		WindowStartHeight:  windowStart,
		Span:               span,
		MissedCount:        1,
		MissedBlocksBitmap: []byte{0x00, 0x08}, // only bit 11 (claimedHeight - windowStart) set
		SlashTokens:        math.NewInt(1000),
		MaturesAt:          ctx.BlockTime().Add(time.Hour),
	}
	pendingKey := collections.Join3(cid, providerAddr.ToSdkConsAddr().Bytes(), claimedHeight)
	require.NoError(t, k.PendingDowntimeSlashes.Set(ctx, pendingKey, pending))

	blockID := cryptotestutil.MakeBlockID([]byte("blockhash"), 1, []byte("partshash"))
	vote, err := tmtypes.MakeVote(signer, challengeTestChainID, 0, claimedHeight, 0, tmproto.PrecommitType, blockID, ctx.BlockTime())
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
				ChainID:        challengeTestChainID,
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

	// No withheld fee record is seeded, so PayWithheldFees has nothing to
	// iterate and this test needs no bank/staking mocks.
	require.NoError(t, k.HandleChallengeConsumerDowntime(ctx, msg))

	require.Equal(t, types.CONSUMER_PHASE_PAUSED, k.GetConsumerPhase(ctx, cid))
	_, err = k.PendingDowntimeSlashes.Get(ctx, pendingKey)
	require.ErrorIs(t, err, collections.ErrNotFound, "pending slash must be cancelled")
}

// TestHandleChallengeConsumerDowntime_SuccessIsolatesOtherConsumer extends the
// happy path with a second, independent consumer B that carries its own
// pending downtime slash and withheld fee record for the *same* validator
// address (keyed under a different consumer id): consumer A's successful
// challenge -- and the CancelConsumerDowntimeState it triggers -- must not
// touch consumer B's state, proving the per-consumer scoping isn't
// accidentally keyed by validator address alone.
func TestHandleChallengeConsumerDowntime_SuccessIsolatesOtherConsumer(t *testing.T) {
	f := newChallengeFixture(t)

	valAddrCodec := address.NewBech32Codec("cosmosvaloper")
	f.mocks.MockStakingKeeper.EXPECT().ValidatorAddressCodec().Return(valAddrCodec).AnyTimes()

	val, valBytes := newBondedValidator(t, valAddrCodec, 7)
	f.mocks.MockStakingKeeper.EXPECT().
		GetValidatorByConsAddr(gomock.Any(), f.providerAddr.ToSdkConsAddr()).
		Return(val, nil)

	poolAddr := f.k.GetConsumerFeePoolAddress(f.cid)
	amount := sdk.NewInt64Coin("uphoton", 500)
	f.mocks.MockBankKeeper.EXPECT().
		GetBalance(gomock.Any(), poolAddr, "uphoton").
		Return(sdk.NewInt64Coin("uphoton", 1000))
	f.mocks.MockBankKeeper.EXPECT().
		InputOutputCoins(gomock.Any(),
			banktypes.Input{Address: poolAddr.String(), Coins: sdk.NewCoins(amount)},
			[]banktypes.Output{{Address: accAddr(valBytes), Coins: sdk.NewCoins(amount)}},
		).Return(nil)

	cidB := f.k.FetchAndIncrementConsumerId(f.ctx)
	f.k.SetConsumerPhase(f.ctx, cidB, types.CONSUMER_PHASE_LAUNCHED)
	pairKeyB := collections.Join(cidB, f.providerAddr.ToSdkConsAddr().Bytes())
	pendingKeyB := collections.Join3(cidB, f.providerAddr.ToSdkConsAddr().Bytes(), int64(100))
	putPendingDowntimeSlash(t, f.k, f.ctx, cidB, f.providerAddr, math.NewInt(200), f.ctx.BlockTime().Add(time.Hour), 100)
	require.NoError(t, f.k.WithheldFeeRecords.Set(f.ctx, pairKeyB, types.WithheldFeeRecord{
		ConsumerId:       cidB,
		ProviderConsAddr: f.providerAddr.ToSdkConsAddr().Bytes(),
		Amount:           sdk.NewInt64Coin("uphoton", 999),
		ExpiresAt:        f.ctx.BlockTime().Add(time.Hour),
	}))

	require.NoError(t, f.k.HandleChallengeConsumerDowntime(f.ctx, f.msg))

	// consumer A: challenge succeeded as usual.
	_, err := f.k.PendingDowntimeSlashes.Get(f.ctx, f.pendingSlashKey())
	require.ErrorIs(t, err, collections.ErrNotFound)
	require.Equal(t, types.CONSUMER_PHASE_PAUSED, f.k.GetConsumerPhase(f.ctx, f.cid))

	// consumer B: entirely untouched.
	require.Equal(t, types.CONSUMER_PHASE_LAUNCHED, f.k.GetConsumerPhase(f.ctx, cidB))
	pendingB, err := f.k.PendingDowntimeSlashes.Get(f.ctx, pendingKeyB)
	require.NoError(t, err, "consumer B's pending downtime slash must survive consumer A's successful challenge")
	require.True(t, math.NewInt(200).Equal(pendingB.SlashTokens))
	hasB, err := f.k.WithheldFeeRecords.Has(f.ctx, pairKeyB)
	require.NoError(t, err)
	require.True(t, hasB, "consumer B's withheld fee record must survive consumer A's successful challenge")
}

// TestHandleChallengeConsumerDowntime_NoPendingSlash verifies step 1: without
// a pending slash for the accused validator, the challenge is rejected
// before touching any header/commit/signature verification.
func TestHandleChallengeConsumerDowntime_NoPendingSlash(t *testing.T) {
	f := newChallengeFixture(t)
	require.NoError(t, f.k.PendingDowntimeSlashes.Remove(f.ctx, f.pendingSlashKey()))

	err := f.k.HandleChallengeConsumerDowntime(f.ctx, f.msg)
	require.ErrorIs(t, err, types.ErrDowntimeChallengeFailed)
	require.ErrorContains(t, err, "no pending downtime slash")
	require.Equal(t, types.CONSUMER_PHASE_LAUNCHED, f.k.GetConsumerPhase(f.ctx, f.cid))
}

// TestHandleChallengeConsumerDowntime_ClaimedHeightOutsideWindow verifies the
// bounds check on step 1's bitmap index: a claimed_height before
// WindowStartHeight or at/after WindowStartHeight+Span must be rejected
// without touching the bitmap.
func TestHandleChallengeConsumerDowntime_ClaimedHeightOutsideWindow(t *testing.T) {
	for _, tc := range []struct {
		name          string
		claimedHeight int64
	}{
		{"before window start", 99},
		{"at window end (exclusive)", 105},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newChallengeFixture(t)
			f.msg.ClaimedHeight = tc.claimedHeight

			err := f.k.HandleChallengeConsumerDowntime(f.ctx, f.msg)
			require.ErrorIs(t, err, types.ErrDowntimeChallengeFailed)
			// A height outside every pending window rejects at the
			// window-lookup step, not the bitmap check.
			require.ErrorContains(t, err, "no pending downtime slash")
			f.requireNoStateChange(t)
		})
	}
}

// TestHandleChallengeConsumerDowntime_BitmapDoesNotClaimHeight verifies step
// 1's bit check: claimed_height is within the window but the bitmap does not
// mark it missed.
func TestHandleChallengeConsumerDowntime_BitmapDoesNotClaimHeight(t *testing.T) {
	f := newChallengeFixture(t)

	pending, err := f.k.PendingDowntimeSlashes.Get(f.ctx, f.pendingSlashKey())
	require.NoError(t, err)
	pending.MissedBlocksBitmap = []byte{0x00}
	require.NoError(t, f.k.PendingDowntimeSlashes.Set(f.ctx, f.pendingSlashKey(), pending))

	err = f.k.HandleChallengeConsumerDowntime(f.ctx, f.msg)
	require.ErrorIs(t, err, types.ErrDowntimeChallengeFailed)
	require.ErrorContains(t, err, "is not marked missed")
	f.requireNoStateChange(t)
}

// TestHandleChallengeConsumerDowntime_WrongHeaderHeight verifies step 2:
// the header must be for claimed_height+1.
func TestHandleChallengeConsumerDowntime_WrongHeaderHeight(t *testing.T) {
	f := newChallengeFixture(t)
	f.msg.Header.Header.Height = f.claimedHeight // should be claimedHeight+1

	err := f.k.HandleChallengeConsumerDowntime(f.ctx, f.msg)
	require.ErrorIs(t, err, types.ErrDowntimeChallengeFailed)
	require.ErrorContains(t, err, "is not claimed_height+1")
	f.requireNoStateChange(t)
}

// TestHandleChallengeConsumerDowntime_WrongHeaderChainID verifies step 2: the
// header's chain id must match the consumer's registered chain id.
func TestHandleChallengeConsumerDowntime_WrongHeaderChainID(t *testing.T) {
	f := newChallengeFixture(t)
	f.msg.Header.Header.ChainID = "some-other-chain-1"

	err := f.k.HandleChallengeConsumerDowntime(f.ctx, f.msg)
	require.ErrorIs(t, err, types.ErrDowntimeChallengeFailed)
	require.ErrorContains(t, err, "does not match consumer chain id")
	f.requireNoStateChange(t)
}

// TestHandleChallengeConsumerDowntime_MalformedHeaderNoPanic verifies the
// handler's defensive nil-check: a header shaped like &ibctmtypes.Header{}
// (no SignedHeader at all) must be rejected with a typed error instead of
// nil-pointer-panicking on msg.Header.Header.ChainID. ValidateBasic already
// rejects this shape, but this permissionless entry point guards against it
// again in case ValidateBasic is ever bypassed or this handler is called
// directly, as it is here.
func TestHandleChallengeConsumerDowntime_MalformedHeaderNoPanic(t *testing.T) {
	f := newChallengeFixture(t)
	f.msg.Header = &ibctmtypes.Header{}

	require.NotPanics(t, func() {
		err := f.k.HandleChallengeConsumerDowntime(f.ctx, f.msg)
		require.ErrorIs(t, err, types.ErrDowntimeChallengeFailed)
		require.ErrorContains(t, err, "header is malformed")
	})
	f.requireNoStateChange(t)
}

// TestHandleChallengeConsumerDowntime_LastCommitHashMismatch verifies step 4:
// the supplied commit must actually be the one sealed into the header via
// LastCommitHash.
func TestHandleChallengeConsumerDowntime_LastCommitHashMismatch(t *testing.T) {
	f := newChallengeFixture(t)
	f.msg.Header.Header.LastCommitHash = []byte("not-the-real-commit-hash-000000")

	err := f.k.HandleChallengeConsumerDowntime(f.ctx, f.msg)
	require.ErrorIs(t, err, types.ErrDowntimeChallengeFailed)
	require.ErrorContains(t, err, "last_commit hash does not match header LastCommitHash")
	f.requireNoStateChange(t)
}

// TestHandleChallengeConsumerDowntime_LastCommitWrongHeight verifies step 4's
// height check: the commit must be for claimed_height.
func TestHandleChallengeConsumerDowntime_LastCommitWrongHeight(t *testing.T) {
	f := newChallengeFixture(t)
	f.msg.LastCommit.Height = f.claimedHeight + 1

	err := f.k.HandleChallengeConsumerDowntime(f.ctx, f.msg)
	require.ErrorIs(t, err, types.ErrDowntimeChallengeFailed)
	require.ErrorContains(t, err, "does not match claimed_height")
	f.requireNoStateChange(t)
}

// TestHandleChallengeConsumerDowntime_PubkeyAddressMismatch verifies step 5's
// self-authentication check: validator_pubkey must derive validator_addr.
func TestHandleChallengeConsumerDowntime_PubkeyAddressMismatch(t *testing.T) {
	f := newChallengeFixture(t)

	other := tmtypes.NewMockPV()
	otherPubKey, err := other.GetPubKey()
	require.NoError(t, err)
	f.msg.ValidatorPubkey = otherPubKey.Bytes()

	err = f.k.HandleChallengeConsumerDowntime(f.ctx, f.msg)
	require.ErrorIs(t, err, types.ErrDowntimeChallengeFailed)
	require.ErrorContains(t, err, "validator_pubkey does not derive validator_addr")
	f.requireNoStateChange(t)
}

// TestHandleChallengeConsumerDowntime_Absent verifies step 5: a commit that
// does not carry a Commit-or-Nil signature for the accused validator (here,
// no signature at all -- the honest representation of "this validator did
// not vote", since a real Absent CommitSig carries no validator address to
// match against) fails to disprove the claimed downtime.
func TestHandleChallengeConsumerDowntime_Absent(t *testing.T) {
	f := newChallengeFixture(t)

	commit := &tmtypes.Commit{
		Height:  f.claimedHeight,
		Round:   0,
		BlockID: cryptotestutil.MakeBlockID([]byte("blockhash"), 1, []byte("partshash")),
		Signatures: []tmtypes.CommitSig{
			tmtypes.NewCommitSigAbsent(),
		},
	}
	f.msg.Header.Header.LastCommitHash = commit.Hash()
	f.msg.LastCommit = commit.ToProto()

	err := f.k.HandleChallengeConsumerDowntime(f.ctx, f.msg)
	require.ErrorIs(t, err, types.ErrDowntimeChallengeFailed)
	require.ErrorContains(t, err, "carries no signature for validator_addr")
	f.requireNoStateChange(t)
}

// TestHandleChallengeConsumerDowntime_ForgedSignature verifies step 5's
// signature check: a CommitSig with the right address and flag but a bogus
// signature blob must not verify.
func TestHandleChallengeConsumerDowntime_ForgedSignature(t *testing.T) {
	f := newChallengeFixture(t)

	forged := f.msg.LastCommit.Signatures[0]
	forged.Signature = append([]byte(nil), forged.Signature...)
	forged.Signature[0] ^= 0xFF // flip a byte: still well-formed, no longer a valid signature
	f.msg.LastCommit.Signatures[0] = forged

	// LastCommitHash must still match: forging the sig, not swapping commits.
	commit, err := tmtypes.CommitFromProto(f.msg.LastCommit)
	require.NoError(t, err)
	f.msg.Header.Header.LastCommitHash = commit.Hash()

	err = f.k.HandleChallengeConsumerDowntime(f.ctx, f.msg)
	require.ErrorIs(t, err, types.ErrDowntimeChallengeFailed)
	require.ErrorContains(t, err, "does not verify against validator_pubkey")
	f.requireNoStateChange(t)
}
