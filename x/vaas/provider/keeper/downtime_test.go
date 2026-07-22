package keeper_test

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"

	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	tmtypes "github.com/cometbft/cometbft/types"

	testkeeper "github.com/allinbits/vaas/testutil/keeper"
	providerkeeper "github.com/allinbits/vaas/x/vaas/provider/keeper"
	"github.com/allinbits/vaas/x/vaas/provider/types"
	vaastypes "github.com/allinbits/vaas/x/vaas/types"
)

// setupDowntimeTest builds a launched consumer with a registered client and
// validator, wires infractionParams and spawnTime, and overrides
// windowEndTimestamp resolution to return windowEndTime (see
// Keeper.OverrideWindowEndTimestampForTest) -- so tests exercising the
// grace-period and evidence-age checks don't need to fabricate real IBC
// consensus states. Returns the keeper, context, mock controller, mocks, the
// consumer id, and the validator's provider consensus address.
func setupDowntimeTest(
	t *testing.T,
	infractionParams types.InfractionParameters,
	spawnTime time.Time,
	windowEndTime time.Time,
) (providerkeeper.Keeper, sdk.Context, *gomock.Controller, testkeeper.MockedKeepers, uint64, types.ProviderConsAddress) {
	t.Helper()
	keeperParams := testkeeper.NewInMemKeeperParams(t)
	providerKeeper, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, keeperParams)

	consumerId := uint64(0)
	providerKeeper.SetConsumerPhase(ctx, consumerId, types.CONSUMER_PHASE_LAUNCHED)
	providerKeeper.SetConsumerChainId(ctx, consumerId, "consumer-chain")
	providerKeeper.SetConsumerClientId(ctx, consumerId, "07-tendermint-0")
	providerKeeper.SetEquivocationEvidenceMinHeight(ctx, consumerId, 1)
	providerKeeper.SetInfractionParams(ctx, infractionParams)
	require.NoError(t, providerKeeper.SetConsumerInitializationParameters(ctx, consumerId, types.ConsumerInitializationParameters{
		SpawnTime: spawnTime,
	}))

	pubKey, err := cryptocodec.FromCmtPubKeyInterface(tmtypes.NewMockPV().PrivKey.PubKey())
	require.NoError(t, err)
	validator, err := stakingtypes.NewValidator(
		sdk.ValAddress(pubKey.Address()).String(),
		pubKey,
		stakingtypes.NewDescription("", "", "", "", ""),
	)
	require.NoError(t, err)
	consAddr, err := validator.GetConsAddr()
	require.NoError(t, err)
	providerAddr := types.NewProviderConsAddress(consAddr)
	cmtPubKey, err := validator.CmtConsPublicKey()
	require.NoError(t, err)
	require.NoError(t, providerKeeper.SetConsumerValidator(ctx, consumerId, types.ConsensusValidator{
		ProviderConsAddr: consAddr,
		Power:            1000,
		PublicKey:        &cmtPubKey,
		JoinHeight:       1,
	}))

	providerKeeper.OverrideWindowEndTimestampForTest(func(_ sdk.Context, _ string, _ int64) (time.Time, error) {
		return windowEndTime, nil
	})

	return providerKeeper, ctx, ctrl, mocks, consumerId, providerAddr
}

// downtimeParams returns InfractionParameters carrying only the fields
// HandleConsumerDowntime reads; DoubleSign/Downtime slash-jail params are
// left nil since the downtime path queues a PendingDowntimeSlash instead of
// slashing directly.
func downtimeParams(window int64, minSigned string, gracePeriod, challengeWindow, maxAge time.Duration) types.InfractionParameters {
	return types.InfractionParameters{
		SignedBlocksWindow:      window,
		MinSignedPerWindow:      math.LegacyMustNewDecFromStr(minSigned),
		DowntimeGracePeriod:     gracePeriod,
		DowntimeChallengeWindow: challengeWindow,
		DowntimeEvidenceMaxAge:  maxAge,
	}
}

func TestHandleConsumerDowntimeAcceptsAndQueuesPricedSlash(t *testing.T) {
	windowEndTime := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	spawnTime := windowEndTime.Add(-30 * 24 * time.Hour)
	infractionParams := downtimeParams(8, "0.5", 0, 7*24*time.Hour, 72*time.Hour)

	providerKeeper, ctx, ctrl, mocks, consumerId, providerAddr := setupDowntimeTest(t, infractionParams, spawnTime, windowEndTime)
	defer ctrl.Finish()
	ctx = ctx.WithBlockTime(windowEndTime)

	// P=1000uphoton, resolved from an epoch record covering this window; C=2.
	providerKeeper.SetEpochShareRecord(ctx, consumerId, windowEndTime, math.NewInt(1000))
	mocks.MockPhotonKeeper.EXPECT().ConversionRate(ctx).Return(math.LegacyNewDec(2), nil)

	consAddr := providerAddr.ToSdkConsAddr()
	evidencePacket := vaastypes.NewEvidencePacketData(
		sdk.ConsAddress(consAddr),
		93,
		[]byte{0x3F}, // 6 of 8 missed; maxMissed for window=8,minSigned=0.5 is 4
		8,
		8,
		math.LegacyMustNewDecFromStr("0.5"),
	)

	err := providerKeeper.HandleConsumerEvidencePacket(ctx, consumerId, evidencePacket)
	require.NoError(t, err)

	require.True(t, providerKeeper.IsEpochDowntime(ctx, consumerId, consAddr))

	// windowStart 93, span 8 => window end (WindowEndHeight) 100.
	pending, err := providerKeeper.PendingDowntimeSlashes.Get(ctx, collections.Join3(consumerId, consAddr.Bytes(), int64(100)))
	require.NoError(t, err)
	require.Equal(t, int64(93), pending.WindowStartHeight)
	require.Equal(t, int64(8), pending.Span)
	require.Equal(t, int64(6), pending.MissedCount)
	// M = 6/8 = 0.75; slashTokens = P*M/C = 1000*0.75/2 = 375
	require.True(t, math.NewInt(375).Equal(pending.SlashTokens), "expected 375, got %s", pending.SlashTokens)
	require.Equal(t, windowEndTime.Add(7*24*time.Hour), pending.MaturesAt)
}

func TestHandleConsumerDowntimeRejectsBelowThreshold(t *testing.T) {
	windowEndTime := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	spawnTime := windowEndTime.Add(-30 * 24 * time.Hour)
	infractionParams := downtimeParams(8, "0.5", 0, 7*24*time.Hour, 72*time.Hour)

	providerKeeper, ctx, ctrl, _, consumerId, providerAddr := setupDowntimeTest(t, infractionParams, spawnTime, windowEndTime)
	defer ctrl.Finish()
	ctx = ctx.WithBlockTime(windowEndTime)

	evidencePacket := vaastypes.NewEvidencePacketData(
		sdk.ConsAddress(providerAddr.ToSdkConsAddr()),
		93,
		[]byte{0x07}, // 3 of 8 missed; maxMissed is 4, so this does not qualify
		8,
		8,
		math.LegacyMustNewDecFromStr("0.5"),
	)

	err := providerKeeper.HandleConsumerEvidencePacket(ctx, consumerId, evidencePacket)
	require.Error(t, err)
	require.Contains(t, err.Error(), "does not exceed the infraction threshold")
}

// TestHandleConsumerDowntimeRejectsExactlyAtThreshold pins the strictness of
// the infraction threshold comparison: a window missing exactly MaxMissed()
// blocks (4 of 8 with min-signed 0.5) does not qualify as downtime -- only
// exceeding the threshold does.
func TestHandleConsumerDowntimeRejectsExactlyAtThreshold(t *testing.T) {
	windowEndTime := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	spawnTime := windowEndTime.Add(-30 * 24 * time.Hour)
	infractionParams := downtimeParams(8, "0.5", 0, 7*24*time.Hour, 72*time.Hour)

	providerKeeper, ctx, ctrl, _, consumerId, providerAddr := setupDowntimeTest(t, infractionParams, spawnTime, windowEndTime)
	defer ctrl.Finish()
	ctx = ctx.WithBlockTime(windowEndTime)

	evidencePacket := vaastypes.NewEvidencePacketData(
		sdk.ConsAddress(providerAddr.ToSdkConsAddr()),
		93,
		[]byte{0x0F}, // exactly 4 of 8 missed == maxMissed: at the threshold, not past it
		8,
		8,
		math.LegacyMustNewDecFromStr("0.5"),
	)

	err := providerKeeper.HandleConsumerEvidencePacket(ctx, consumerId, evidencePacket)
	require.Error(t, err)
	require.Contains(t, err.Error(), "does not exceed the infraction threshold")
}

func TestHandleConsumerEvidencePacketRejectsSpanExceedingWindow(t *testing.T) {
	keeperParams := testkeeper.NewInMemKeeperParams(t)
	providerKeeper, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, keeperParams)
	defer ctrl.Finish()

	consumerId := uint64(0)
	providerKeeper.SetConsumerPhase(ctx, consumerId, types.CONSUMER_PHASE_LAUNCHED)

	// Span (8) exceeds the echoed window (4); rejected by
	// EvidencePacketData.Validate() before HandleConsumerDowntime ever runs.
	evidencePacket := vaastypes.EvidencePacketData{
		ValidatorAddr:      sdk.ConsAddress([]byte{0x01, 0x02, 0x03}),
		WindowEndHeight:    107,
		Infraction:         stakingtypes.Infraction_INFRACTION_DOWNTIME,
		WindowStartHeight:  100,
		MissedBlocksBitmap: []byte{0xFF},
		SignedBlocksWindow: 4,
		MinSignedPerWindow: math.LegacyMustNewDecFromStr("0.5"),
	}

	err := providerKeeper.HandleConsumerEvidencePacket(ctx, consumerId, evidencePacket)
	require.Error(t, err)
	require.Contains(t, err.Error(), "window span cannot exceed")
}

func TestHandleConsumerDowntimeRejectsUnacceptableParams(t *testing.T) {
	windowEndTime := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	spawnTime := windowEndTime.Add(-30 * 24 * time.Hour)
	infractionParams := downtimeParams(8, "0.5", 0, 7*24*time.Hour, 72*time.Hour)

	providerKeeper, ctx, ctrl, _, consumerId, providerAddr := setupDowntimeTest(t, infractionParams, spawnTime, windowEndTime)
	defer ctrl.Finish()
	ctx = ctx.WithBlockTime(windowEndTime)

	// Echoes a window (10) that matches neither the current params (8) nor
	// any recorded previous params.
	evidencePacket := vaastypes.NewEvidencePacketData(
		sdk.ConsAddress(providerAddr.ToSdkConsAddr()),
		93,
		[]byte{0x3F},
		8,
		10,
		math.LegacyMustNewDecFromStr("0.5"),
	)

	err := providerKeeper.HandleConsumerEvidencePacket(ctx, consumerId, evidencePacket)
	require.Error(t, err)
	require.Contains(t, err.Error(), "unacceptable downtime params")
}

// TestHandleConsumerDowntimeAcceptsMultipleDisjointPendingWindows asserts the
// core multi-window behavior: a second, disjoint window for the same
// (consumer, validator) pair is accepted -- and priced independently -- while
// the first slash is still pending. Both entries coexist in
// PendingDowntimeSlashes, keyed by their own window-end height.
func TestHandleConsumerDowntimeAcceptsMultipleDisjointPendingWindows(t *testing.T) {
	windowEndTime := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	spawnTime := windowEndTime.Add(-30 * 24 * time.Hour)
	infractionParams := downtimeParams(8, "0.5", 0, 7*24*time.Hour, 72*time.Hour)

	providerKeeper, ctx, ctrl, mocks, consumerId, providerAddr := setupDowntimeTest(t, infractionParams, spawnTime, windowEndTime)
	defer ctrl.Finish()
	ctx = ctx.WithBlockTime(windowEndTime)

	providerKeeper.SetEpochShareRecord(ctx, consumerId, windowEndTime, math.NewInt(1000))
	mocks.MockPhotonKeeper.EXPECT().ConversionRate(ctx).Return(math.LegacyNewDec(2), nil).Times(2)

	consAddr := providerAddr.ToSdkConsAddr()
	// windowStart 93, span 8 => window end 100.
	firstPacket := vaastypes.NewEvidencePacketData(sdk.ConsAddress(consAddr), 93, []byte{0x3F}, 8, 8, math.LegacyMustNewDecFromStr("0.5"))
	require.NoError(t, providerKeeper.HandleConsumerEvidencePacket(ctx, consumerId, firstPacket))

	// windowStart 101 > 100: disjoint from the first, accepted while it is
	// still pending.
	secondPacket := vaastypes.NewEvidencePacketData(sdk.ConsAddress(consAddr), 101, []byte{0x3F}, 8, 8, math.LegacyMustNewDecFromStr("0.5"))
	require.NoError(t, providerKeeper.HandleConsumerEvidencePacket(ctx, consumerId, secondPacket))

	first, err := providerKeeper.PendingDowntimeSlashes.Get(ctx, collections.Join3(consumerId, consAddr.Bytes(), int64(100)))
	require.NoError(t, err, "first window must still be pending")
	require.Equal(t, int64(93), first.WindowStartHeight)

	second, err := providerKeeper.PendingDowntimeSlashes.Get(ctx, collections.Join3(consumerId, consAddr.Bytes(), int64(108)))
	require.NoError(t, err, "second, disjoint window must also be pending")
	require.Equal(t, int64(101), second.WindowStartHeight)
}

// TestHandleConsumerDowntimeAcceptsDisjointWindowsOutOfOrder asserts that
// acceptance is order-independent: the later window [101, 108] arriving
// before the earlier window [93, 100] does not void it -- both are accepted,
// each queues its own priced pending slash. In between, a window
// intersecting the already-accepted later window from below ([95, 102]) is
// rejected, covering the direction where the accepted record sits above the
// incoming window.
func TestHandleConsumerDowntimeAcceptsDisjointWindowsOutOfOrder(t *testing.T) {
	windowEndTime := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	spawnTime := windowEndTime.Add(-30 * 24 * time.Hour)
	infractionParams := downtimeParams(8, "0.5", 0, 7*24*time.Hour, 72*time.Hour)

	providerKeeper, ctx, ctrl, mocks, consumerId, providerAddr := setupDowntimeTest(t, infractionParams, spawnTime, windowEndTime)
	defer ctrl.Finish()
	ctx = ctx.WithBlockTime(windowEndTime)

	providerKeeper.SetEpochShareRecord(ctx, consumerId, windowEndTime, math.NewInt(1000))
	mocks.MockPhotonKeeper.EXPECT().ConversionRate(ctx).Return(math.LegacyNewDec(2), nil).Times(2)

	consAddr := providerAddr.ToSdkConsAddr()
	// The later window [101, 108] arrives first.
	laterPacket := vaastypes.NewEvidencePacketData(sdk.ConsAddress(consAddr), 101, []byte{0x3F}, 8, 8, math.LegacyMustNewDecFromStr("0.5"))
	require.NoError(t, providerKeeper.HandleConsumerEvidencePacket(ctx, consumerId, laterPacket))

	// A window [95, 102] intersecting the accepted [101, 108] from below is
	// rejected.
	intersecting := vaastypes.NewEvidencePacketData(sdk.ConsAddress(consAddr), 95, []byte{0x3F}, 8, 8, math.LegacyMustNewDecFromStr("0.5"))
	err := providerKeeper.HandleConsumerEvidencePacket(ctx, consumerId, intersecting)
	require.Error(t, err)
	require.Contains(t, err.Error(), "already accepted")

	// The earlier, disjoint window [93, 100] is still accepted after the
	// later one.
	earlierPacket := vaastypes.NewEvidencePacketData(sdk.ConsAddress(consAddr), 93, []byte{0x3F}, 8, 8, math.LegacyMustNewDecFromStr("0.5"))
	require.NoError(t, providerKeeper.HandleConsumerEvidencePacket(ctx, consumerId, earlierPacket))

	later, err := providerKeeper.PendingDowntimeSlashes.Get(ctx, collections.Join3(consumerId, consAddr.Bytes(), int64(108)))
	require.NoError(t, err, "later window must be pending")
	require.Equal(t, int64(101), later.WindowStartHeight)
	require.True(t, later.SlashTokens.IsPositive(), "later window must be priced")

	earlier, err := providerKeeper.PendingDowntimeSlashes.Get(ctx, collections.Join3(consumerId, consAddr.Bytes(), int64(100)))
	require.NoError(t, err, "earlier window accepted after the later one must be pending")
	require.Equal(t, int64(93), earlier.WindowStartHeight)
	require.True(t, earlier.SlashTokens.IsPositive(), "earlier window must be priced")

	// Both accepted records are retained, each carrying its own window start.
	laterAccepted, err := providerKeeper.AcceptedDowntimeWindows.Get(ctx, collections.Join3(consumerId, consAddr.Bytes(), int64(108)))
	require.NoError(t, err)
	require.Equal(t, int64(101), laterAccepted.WindowStart)
	earlierAccepted, err := providerKeeper.AcceptedDowntimeWindows.Get(ctx, collections.Join3(consumerId, consAddr.Bytes(), int64(100)))
	require.NoError(t, err)
	require.Equal(t, int64(93), earlierAccepted.WindowStart)
}

// TestHandleConsumerDowntimeRejectsOverlappingWhilePending asserts that a
// window intersecting one still pending for the same (consumer, validator)
// pair is rejected via the AcceptedDowntimeWindows intersection check, the
// same guard that rejects intersection after the earlier slash has matured
// and executed.
func TestHandleConsumerDowntimeRejectsOverlappingWhilePending(t *testing.T) {
	windowEndTime := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	spawnTime := windowEndTime.Add(-30 * 24 * time.Hour)
	infractionParams := downtimeParams(8, "0.5", 0, 7*24*time.Hour, 72*time.Hour)

	providerKeeper, ctx, ctrl, mocks, consumerId, providerAddr := setupDowntimeTest(t, infractionParams, spawnTime, windowEndTime)
	defer ctrl.Finish()
	ctx = ctx.WithBlockTime(windowEndTime)

	providerKeeper.SetEpochShareRecord(ctx, consumerId, windowEndTime, math.NewInt(1000))
	mocks.MockPhotonKeeper.EXPECT().ConversionRate(ctx).Return(math.LegacyNewDec(2), nil)

	consAddr := providerAddr.ToSdkConsAddr()
	firstPacket := vaastypes.NewEvidencePacketData(sdk.ConsAddress(consAddr), 93, []byte{0x3F}, 8, 8, math.LegacyMustNewDecFromStr("0.5"))
	require.NoError(t, providerKeeper.HandleConsumerEvidencePacket(ctx, consumerId, firstPacket))

	// A second window overlapping the first (start 95 <= window end 100) is
	// rejected while the first slash is still pending; ConversionRate is not
	// called again (verified by the single EXPECT() above and ctrl.Finish()).
	secondPacket := vaastypes.NewEvidencePacketData(sdk.ConsAddress(consAddr), 95, []byte{0x3F}, 8, 8, math.LegacyMustNewDecFromStr("0.5"))
	err := providerKeeper.HandleConsumerEvidencePacket(ctx, consumerId, secondPacket)
	require.Error(t, err)
	require.Contains(t, err.Error(), "already accepted")
}

func TestHandleConsumerDowntimeRejectsOverlappingAcceptedWindow(t *testing.T) {
	windowEndTime := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	spawnTime := windowEndTime.Add(-30 * 24 * time.Hour)
	infractionParams := downtimeParams(8, "0.5", 0, 7*24*time.Hour, 72*time.Hour)

	providerKeeper, ctx, ctrl, mocks, consumerId, providerAddr := setupDowntimeTest(t, infractionParams, spawnTime, windowEndTime)
	defer ctrl.Finish()
	ctx = ctx.WithBlockTime(windowEndTime)

	providerKeeper.SetEpochShareRecord(ctx, consumerId, windowEndTime, math.NewInt(1000))
	mocks.MockPhotonKeeper.EXPECT().ConversionRate(ctx).Return(math.LegacyNewDec(2), nil).Times(2)

	consAddr := providerAddr.ToSdkConsAddr()
	// windowStart 93, span 8 => WindowEndHeight (window end) 100.
	pendingKey := collections.Join3(consumerId, consAddr.Bytes(), int64(100))

	firstPacket := vaastypes.NewEvidencePacketData(sdk.ConsAddress(consAddr), 93, []byte{0x3F}, 8, 8, math.LegacyMustNewDecFromStr("0.5"))
	require.NoError(t, providerKeeper.HandleConsumerEvidencePacket(ctx, consumerId, firstPacket))

	// Simulate the pending slash maturing and executing: the BeginBlock sweep
	// removes the PendingDowntimeSlashes entry (see SweepPendingDowntimeSlashes),
	// but the AcceptedDowntimeWindows record must be retained so a
	// re-submission for the same or an intersecting window is still caught.
	require.NoError(t, providerKeeper.PendingDowntimeSlashes.Remove(ctx, pendingKey))

	// An exact duplicate of the accepted window (same window end 100) is
	// rejected even though nothing is pending anymore.
	duplicate := vaastypes.NewEvidencePacketData(sdk.ConsAddress(consAddr), 93, []byte{0x3F}, 8, 8, math.LegacyMustNewDecFromStr("0.5"))
	err := providerKeeper.HandleConsumerEvidencePacket(ctx, consumerId, duplicate)
	require.Error(t, err)
	require.Contains(t, err.Error(), "already accepted")

	// A window [95, 102] starting inside the accepted [93, 100] and ending
	// beyond it is likewise rejected.
	overlapping := vaastypes.NewEvidencePacketData(sdk.ConsAddress(consAddr), 95, []byte{0x3F}, 8, 8, math.LegacyMustNewDecFromStr("0.5"))
	err = providerKeeper.HandleConsumerEvidencePacket(ctx, consumerId, overlapping)
	require.Error(t, err)
	require.Contains(t, err.Error(), "already accepted")

	// A later, disjoint window (start 101 > 100) is accepted.
	later := vaastypes.NewEvidencePacketData(sdk.ConsAddress(consAddr), 101, []byte{0x3F}, 8, 8, math.LegacyMustNewDecFromStr("0.5"))
	require.NoError(t, providerKeeper.HandleConsumerEvidencePacket(ctx, consumerId, later))
}

// TestHandleConsumerDowntimeRejectsWindowStartingAtAcceptedEnd pins the
// closed-interval boundary of the intersection check from below: with
// [93, 100] accepted, new evidence [100, 107] shares exactly the boundary
// height 100 (newStart == retainedEnd) and is rejected.
func TestHandleConsumerDowntimeRejectsWindowStartingAtAcceptedEnd(t *testing.T) {
	windowEndTime := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	spawnTime := windowEndTime.Add(-30 * 24 * time.Hour)
	infractionParams := downtimeParams(8, "0.5", 0, 7*24*time.Hour, 72*time.Hour)

	providerKeeper, ctx, ctrl, mocks, consumerId, providerAddr := setupDowntimeTest(t, infractionParams, spawnTime, windowEndTime)
	defer ctrl.Finish()
	ctx = ctx.WithBlockTime(windowEndTime)

	providerKeeper.SetEpochShareRecord(ctx, consumerId, windowEndTime, math.NewInt(1000))
	mocks.MockPhotonKeeper.EXPECT().ConversionRate(ctx).Return(math.LegacyNewDec(2), nil)

	consAddr := providerAddr.ToSdkConsAddr()
	// windowStart 93, span 8 => window end 100.
	accepted := vaastypes.NewEvidencePacketData(sdk.ConsAddress(consAddr), 93, []byte{0x3F}, 8, 8, math.LegacyMustNewDecFromStr("0.5"))
	require.NoError(t, providerKeeper.HandleConsumerEvidencePacket(ctx, consumerId, accepted))

	// [100, 107] intersects the accepted [93, 100] only at height 100.
	touching := vaastypes.NewEvidencePacketData(sdk.ConsAddress(consAddr), 100, []byte{0x3F}, 8, 8, math.LegacyMustNewDecFromStr("0.5"))
	err := providerKeeper.HandleConsumerEvidencePacket(ctx, consumerId, touching)
	require.Error(t, err)
	require.Contains(t, err.Error(), "already accepted")
}

// TestHandleConsumerDowntimeRejectsWindowEndingAtAcceptedStart pins the
// closed-interval boundary of the intersection check from above: with
// [101, 108] accepted, new evidence [94, 101] shares exactly the boundary
// height 101 (retainedStart == newEnd) and is rejected.
func TestHandleConsumerDowntimeRejectsWindowEndingAtAcceptedStart(t *testing.T) {
	windowEndTime := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	spawnTime := windowEndTime.Add(-30 * 24 * time.Hour)
	infractionParams := downtimeParams(8, "0.5", 0, 7*24*time.Hour, 72*time.Hour)

	providerKeeper, ctx, ctrl, mocks, consumerId, providerAddr := setupDowntimeTest(t, infractionParams, spawnTime, windowEndTime)
	defer ctrl.Finish()
	ctx = ctx.WithBlockTime(windowEndTime)

	providerKeeper.SetEpochShareRecord(ctx, consumerId, windowEndTime, math.NewInt(1000))
	mocks.MockPhotonKeeper.EXPECT().ConversionRate(ctx).Return(math.LegacyNewDec(2), nil)

	consAddr := providerAddr.ToSdkConsAddr()
	// windowStart 101, span 8 => window end 108.
	accepted := vaastypes.NewEvidencePacketData(sdk.ConsAddress(consAddr), 101, []byte{0x3F}, 8, 8, math.LegacyMustNewDecFromStr("0.5"))
	require.NoError(t, providerKeeper.HandleConsumerEvidencePacket(ctx, consumerId, accepted))

	// [94, 101] intersects the accepted [101, 108] only at height 101.
	touching := vaastypes.NewEvidencePacketData(sdk.ConsAddress(consAddr), 94, []byte{0x3F}, 8, 8, math.LegacyMustNewDecFromStr("0.5"))
	err := providerKeeper.HandleConsumerEvidencePacket(ctx, consumerId, touching)
	require.Error(t, err)
	require.Contains(t, err.Error(), "already accepted")
}

func TestHandleConsumerDowntimeRejectsStaleEvidence(t *testing.T) {
	windowEndTime := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	spawnTime := windowEndTime.Add(-30 * 24 * time.Hour)
	infractionParams := downtimeParams(8, "0.5", 0, 7*24*time.Hour, time.Hour)

	providerKeeper, ctx, ctrl, _, consumerId, providerAddr := setupDowntimeTest(t, infractionParams, spawnTime, windowEndTime)
	defer ctrl.Finish()
	// The evidence is submitted 2h after the window ended; max age is 1h.
	ctx = ctx.WithBlockTime(windowEndTime.Add(2 * time.Hour))

	evidencePacket := vaastypes.NewEvidencePacketData(sdk.ConsAddress(providerAddr.ToSdkConsAddr()), 93, []byte{0x3F}, 8, 8, math.LegacyMustNewDecFromStr("0.5"))

	err := providerKeeper.HandleConsumerEvidencePacket(ctx, consumerId, evidencePacket)
	require.Error(t, err)
	require.Contains(t, err.Error(), "too old: window ended")
}

func TestHandleConsumerDowntimeRejectsDuringGracePeriod(t *testing.T) {
	spawnTime := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	gracePeriod := 24 * time.Hour
	windowEndTime := spawnTime.Add(12 * time.Hour) // still within grace period
	infractionParams := downtimeParams(8, "0.5", gracePeriod, 7*24*time.Hour, 72*time.Hour)

	providerKeeper, ctx, ctrl, _, consumerId, providerAddr := setupDowntimeTest(t, infractionParams, spawnTime, windowEndTime)
	defer ctrl.Finish()
	ctx = ctx.WithBlockTime(windowEndTime)

	evidencePacket := vaastypes.NewEvidencePacketData(sdk.ConsAddress(providerAddr.ToSdkConsAddr()), 93, []byte{0x3F}, 8, 8, math.LegacyMustNewDecFromStr("0.5"))

	err := providerKeeper.HandleConsumerEvidencePacket(ctx, consumerId, evidencePacket)
	require.Error(t, err)
	require.Contains(t, err.Error(), "grace period")
}

func TestHandleConsumerDowntimeGracePeriodDisabledSkipsCheck(t *testing.T) {
	spawnTime := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	windowEndTime := spawnTime // would still be "in grace period" if the check ran
	infractionParams := downtimeParams(8, "0.5", 0, 7*24*time.Hour, 72*time.Hour)

	providerKeeper, ctx, ctrl, mocks, consumerId, providerAddr := setupDowntimeTest(t, infractionParams, spawnTime, windowEndTime)
	defer ctrl.Finish()
	ctx = ctx.WithBlockTime(windowEndTime)

	providerKeeper.SetEpochShareRecord(ctx, consumerId, windowEndTime, math.NewInt(1000))
	mocks.MockPhotonKeeper.EXPECT().ConversionRate(ctx).Return(math.LegacyNewDec(2), nil)

	evidencePacket := vaastypes.NewEvidencePacketData(sdk.ConsAddress(providerAddr.ToSdkConsAddr()), 93, []byte{0x3F}, 8, 8, math.LegacyMustNewDecFromStr("0.5"))

	err := providerKeeper.HandleConsumerEvidencePacket(ctx, consumerId, evidencePacket)
	require.NoError(t, err)
}

func TestHandleConsumerDowntimeRejectsUnanchorableWindow(t *testing.T) {
	spawnTime := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	infractionParams := downtimeParams(8, "0.5", 0, 7*24*time.Hour, 72*time.Hour)

	providerKeeper, ctx, ctrl, _, consumerId, providerAddr := setupDowntimeTest(t, infractionParams, spawnTime, time.Time{})
	defer ctrl.Finish()

	providerKeeper.OverrideWindowEndTimestampForTest(func(_ sdk.Context, _ string, _ int64) (time.Time, error) {
		return time.Time{}, fmt.Errorf("cannot anchor evidence window time")
	})

	evidencePacket := vaastypes.NewEvidencePacketData(sdk.ConsAddress(providerAddr.ToSdkConsAddr()), 93, []byte{0x3F}, 8, 8, math.LegacyMustNewDecFromStr("0.5"))

	err := providerKeeper.HandleConsumerEvidencePacket(ctx, consumerId, evidencePacket)
	require.Error(t, err)
	require.Contains(t, err.Error(), "cannot anchor")
}

func TestHandleConsumerDowntimeRejectsTooOldEvidence(t *testing.T) {
	keeperParams := testkeeper.NewInMemKeeperParams(t)
	providerKeeper, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, keeperParams)
	defer ctrl.Finish()

	consumerId := uint64(0)
	providerKeeper.SetConsumerPhase(ctx, consumerId, types.CONSUMER_PHASE_LAUNCHED)
	providerKeeper.SetConsumerChainId(ctx, consumerId, "consumer-chain")
	providerKeeper.SetEquivocationEvidenceMinHeight(ctx, consumerId, 200)
	providerKeeper.SetInfractionParams(ctx, downtimeParams(8, "0.5", 0, 7*24*time.Hour, 72*time.Hour))

	evidencePacket := vaastypes.NewEvidencePacketData(
		sdk.ConsAddress([]byte{0x01, 0x02, 0x03}),
		93, // window end height 100, below min height of 200
		[]byte{0x3F},
		8,
		8,
		math.LegacyMustNewDecFromStr("0.5"),
	)

	err := providerKeeper.HandleConsumerEvidencePacket(ctx, consumerId, evidencePacket)
	require.Error(t, err)
	require.Contains(t, err.Error(), "too old")
}

func TestHandleConsumerDowntimeRejectsNoClient(t *testing.T) {
	keeperParams := testkeeper.NewInMemKeeperParams(t)
	providerKeeper, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, keeperParams)
	defer ctrl.Finish()

	consumerId := uint64(0)
	providerKeeper.SetConsumerPhase(ctx, consumerId, types.CONSUMER_PHASE_LAUNCHED)
	providerKeeper.SetConsumerChainId(ctx, consumerId, "consumer-chain")
	providerKeeper.SetEquivocationEvidenceMinHeight(ctx, consumerId, 1)
	providerKeeper.SetInfractionParams(ctx, downtimeParams(8, "0.5", 0, 7*24*time.Hour, 72*time.Hour))

	pubKey, _ := cryptocodec.FromCmtPubKeyInterface(tmtypes.NewMockPV().PrivKey.PubKey())
	validator, err := stakingtypes.NewValidator(sdk.ValAddress(pubKey.Address()).String(), pubKey, stakingtypes.NewDescription("", "", "", "", ""))
	require.NoError(t, err)
	consAddr, _ := validator.GetConsAddr()
	cmtPubKey, _ := validator.CmtConsPublicKey()
	require.NoError(t, providerKeeper.SetConsumerValidator(ctx, consumerId, types.ConsensusValidator{
		ProviderConsAddr: consAddr,
		Power:            1000,
		PublicKey:        &cmtPubKey,
		JoinHeight:       1,
	}))

	// No client id is registered for this consumer.
	evidencePacket := vaastypes.NewEvidencePacketData(sdk.ConsAddress(consAddr), 93, []byte{0x3F}, 8, 8, math.LegacyMustNewDecFromStr("0.5"))

	err = providerKeeper.HandleConsumerEvidencePacket(ctx, consumerId, evidencePacket)
	require.Error(t, err)
	require.Contains(t, err.Error(), "no IBC client found")
}

func TestHandleConsumerDowntimeRejectsValidatorNotInSet(t *testing.T) {
	keeperParams := testkeeper.NewInMemKeeperParams(t)
	providerKeeper, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, keeperParams)
	defer ctrl.Finish()

	consumerId := uint64(0)
	providerKeeper.SetConsumerPhase(ctx, consumerId, types.CONSUMER_PHASE_LAUNCHED)
	providerKeeper.SetConsumerChainId(ctx, consumerId, "consumer-chain")
	providerKeeper.SetConsumerClientId(ctx, consumerId, "07-tendermint-0")
	providerKeeper.SetEquivocationEvidenceMinHeight(ctx, consumerId, 1)
	providerKeeper.SetInfractionParams(ctx, downtimeParams(8, "0.5", 0, 7*24*time.Hour, 72*time.Hour))

	// The reporting validator is never registered in the consumer's
	// validator set.
	evidencePacket := vaastypes.NewEvidencePacketData(sdk.ConsAddress([]byte{0x01, 0x02, 0x03}), 93, []byte{0x3F}, 8, 8, math.LegacyMustNewDecFromStr("0.5"))

	err := providerKeeper.HandleConsumerEvidencePacket(ctx, consumerId, evidencePacket)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not in the validator set")
}

func TestHandleConsumerEvidencePacketRejectsDoubleSign(t *testing.T) {
	keeperParams := testkeeper.NewInMemKeeperParams(t)
	providerKeeper, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, keeperParams)
	defer ctrl.Finish()

	consumerId := uint64(0)
	providerKeeper.SetConsumerPhase(ctx, consumerId, types.CONSUMER_PHASE_LAUNCHED)

	evidencePacket := vaastypes.EvidencePacketData{
		ValidatorAddr:      sdk.ConsAddress([]byte{0x01, 0x02, 0x03}),
		WindowEndHeight:    100,
		Infraction:         stakingtypes.Infraction_INFRACTION_DOUBLE_SIGN,
		WindowStartHeight:  100,
		MissedBlocksBitmap: []byte{0x01},
		SignedBlocksWindow: 600,
		MinSignedPerWindow: math.LegacyMustNewDecFromStr("0.5"),
	}

	err := providerKeeper.HandleConsumerEvidencePacket(ctx, consumerId, evidencePacket)
	require.Error(t, err)
}

func TestHandleConsumerEvidencePacketRejectsNonLaunchedConsumer(t *testing.T) {
	keeperParams := testkeeper.NewInMemKeeperParams(t)
	providerKeeper, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, keeperParams)
	defer ctrl.Finish()

	consumerId := uint64(0)
	providerKeeper.SetConsumerPhase(ctx, consumerId, types.CONSUMER_PHASE_REGISTERED)

	evidencePacket := vaastypes.NewEvidencePacketData(
		sdk.ConsAddress([]byte{0x01, 0x02, 0x03}),
		100,
		[]byte{0x01},
		1,
		600,
		math.LegacyMustNewDecFromStr("0.5"),
	)

	err := providerKeeper.HandleConsumerEvidencePacket(ctx, consumerId, evidencePacket)
	require.Error(t, err)
}

// TestHandleConsumerEvidencePacketRejectsPausedConsumer verifies that a paused
// consumer's evidence packets are rejected just like any other non-launched
// phase: while a consumer is paused there is no active downtime challenge
// window to accept new evidence into.
func TestHandleConsumerEvidencePacketRejectsPausedConsumer(t *testing.T) {
	keeperParams := testkeeper.NewInMemKeeperParams(t)
	providerKeeper, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, keeperParams)
	defer ctrl.Finish()

	consumerId := uint64(0)
	providerKeeper.SetConsumerPhase(ctx, consumerId, types.CONSUMER_PHASE_PAUSED)

	evidencePacket := vaastypes.NewEvidencePacketData(
		sdk.ConsAddress([]byte{0x01, 0x02, 0x03}),
		100,
		[]byte{0x01},
		1,
		600,
		math.LegacyMustNewDecFromStr("0.5"),
	)

	err := providerKeeper.HandleConsumerEvidencePacket(ctx, consumerId, evidencePacket)
	require.Error(t, err)
}

func TestEvidencePacketDataJSONRoundTrip(t *testing.T) {
	addr := sdk.ConsAddress([]byte{0x01, 0x02, 0x03, 0x04, 0x05})
	packet := vaastypes.NewEvidencePacketData(addr, 42, []byte{0x01}, 1, 600, math.LegacyMustNewDecFromStr("0.5"))

	bz := packet.GetBytes()

	var decoded vaastypes.EvidencePacketData
	err := json.Unmarshal(bz, &decoded)
	require.NoError(t, err)
	require.Equal(t, packet.ValidatorAddr, decoded.ValidatorAddr)
	require.Equal(t, packet.WindowEndHeight, decoded.WindowEndHeight)
	require.Equal(t, packet.Infraction, decoded.Infraction)
}
