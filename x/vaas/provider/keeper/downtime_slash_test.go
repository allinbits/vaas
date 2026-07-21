package keeper_test

import (
	"errors"
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

// setupSweepTest builds a bonded validator behind providerAddr, wires
// infractionParams, and returns everything a sweep test needs to seed a
// PendingDowntimeSlash entry and assert on the resulting staking calls.
func setupSweepTest(t *testing.T, infractionParams types.InfractionParameters) (
	providerkeeper.Keeper, sdk.Context, *gomock.Controller, testkeeper.MockedKeepers, stakingtypes.Validator, types.ProviderConsAddress,
) {
	t.Helper()
	keeperParams := testkeeper.NewInMemKeeperParams(t)
	providerKeeper, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, keeperParams)
	// The zero-value block time from NewInMemKeeperParams cannot represent
	// MaturesAt.Add(-time.Minute) as a valid protobuf timestamp, so anchor
	// tests to a fixed, unambiguous instant instead.
	ctx = ctx.WithBlockTime(time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC))
	providerKeeper.SetInfractionParams(ctx, infractionParams)

	pubKey, err := cryptocodec.FromCmtPubKeyInterface(tmtypes.NewMockPV().PrivKey.PubKey())
	require.NoError(t, err)
	validator, err := stakingtypes.NewValidator(
		sdk.ValAddress(pubKey.Address()).String(),
		pubKey,
		stakingtypes.NewDescription("", "", "", "", ""),
	)
	require.NoError(t, err)
	validator.Status = stakingtypes.Bonded

	consAddr, err := validator.GetConsAddr()
	require.NoError(t, err)
	providerAddr := types.NewProviderConsAddress(consAddr)

	return providerKeeper, ctx, ctrl, mocks, validator, providerAddr
}

// putPendingDowntimeSlash seeds a PendingDowntimeSlash entry for (consumerId,
// providerAddr, windowEndHeight) maturing at maturesAt with the given
// slashTokens. windowEndHeight is also used as the window's Span (with
// WindowStartHeight 0) so the entry is self-consistent for any test that
// happens to read those fields, and distinct calls for the same pair can use
// distinct windowEndHeight values to seed more than one pending entry.
func putPendingDowntimeSlash(
	t *testing.T,
	k providerkeeper.Keeper,
	ctx sdk.Context,
	consumerId uint64,
	providerAddr types.ProviderConsAddress,
	slashTokens math.Int,
	maturesAt time.Time,
	windowEndHeight int64,
) {
	t.Helper()
	key := collections.Join3(consumerId, providerAddr.ToSdkConsAddr().Bytes(), windowEndHeight)
	require.NoError(t, k.PendingDowntimeSlashes.Set(ctx, key, types.PendingDowntimeSlash{
		ConsumerId:        consumerId,
		ProviderConsAddr:  providerAddr.ToSdkConsAddr().Bytes(),
		WindowStartHeight: 0,
		Span:              windowEndHeight + 1,
		SlashTokens:       slashTokens,
		MaturesAt:         maturesAt,
	}))
}

func expectSlashableStakeLookup(t *testing.T, k providerkeeper.Keeper, mocks testkeeper.MockedKeepers, ctx sdk.Context, validator stakingtypes.Validator, consAddr sdk.ConsAddress, lastPower int64, powerReduction math.Int) {
	t.Helper()
	valAddr, err := k.ValidatorAddressCodec().StringToBytes(validator.GetOperator())
	require.NoError(t, err)
	mocks.MockStakingKeeper.EXPECT().
		GetValidatorByConsAddr(ctx, gomock.Any()).
		Return(validator, nil)
	mocks.MockSlashingKeeper.EXPECT().
		IsTombstoned(ctx, consAddr).
		Return(false)
	mocks.MockStakingKeeper.EXPECT().
		GetUnbondingDelegationsFromValidator(ctx, valAddr).
		Return(nil, nil)
	mocks.MockStakingKeeper.EXPECT().
		GetRedelegationsFromSrcValidator(ctx, valAddr).
		Return(nil, nil)
	mocks.MockStakingKeeper.EXPECT().
		GetLastValidatorPower(ctx, valAddr).
		Return(lastPower, nil)
	mocks.MockStakingKeeper.EXPECT().
		PowerReduction(ctx).
		Return(powerReduction)
}

// TestSweepPendingDowntimeSlashesExecutesMaturedEntry asserts that a matured
// entry is executed via SlashWithInfractionReason with the fraction derived
// from slashTokens/totalTokens, Jail is never called, and the entry is
// deleted afterwards.
func TestSweepPendingDowntimeSlashesExecutesMaturedEntry(t *testing.T) {
	infractionParams := types.InfractionParameters{
		Downtime: &types.SlashJailParameters{
			SlashFraction: math.LegacyNewDecWithPrec(5, 1), // 0.5 cap, does not bind here
			Tombstone:     false,
		},
	}
	k, ctx, ctrl, mocks, validator, providerAddr := setupSweepTest(t, infractionParams)
	defer ctrl.Finish()

	consAddr := providerAddr.ToSdkConsAddr()
	consumerId := uint64(0)
	maturesAt := ctx.BlockTime().Add(-time.Minute)
	putPendingDowntimeSlash(t, k, ctx, consumerId, providerAddr, math.NewInt(100), maturesAt, 100)

	powerReduction := math.NewInt(1)
	expectSlashableStakeLookup(t, k, mocks, ctx, validator, consAddr, 1000, powerReduction)

	// totalTokens = 1000 * 1 = 1000; fraction = 100/1000 = 0.1
	expectedFraction := math.LegacyNewDecWithPrec(1, 1)
	mocks.MockStakingKeeper.EXPECT().
		SlashWithInfractionReason(ctx, consAddr, int64(0), int64(1000), expectedFraction, stakingtypes.Infraction_INFRACTION_DOWNTIME).
		Return(math.NewInt(100), nil)

	k.SweepPendingDowntimeSlashes(ctx)

	has, err := k.PendingDowntimeSlashes.Has(ctx, collections.Join3(consumerId, consAddr.Bytes(), int64(100)))
	require.NoError(t, err)
	require.False(t, has, "matured entry should be deleted after execution")
}

// TestSweepPendingDowntimeSlashesSkipsUnmaturedEntry asserts that entries
// whose MaturesAt is still in the future are left untouched: no staking
// calls happen and the entry survives the sweep.
func TestSweepPendingDowntimeSlashesSkipsUnmaturedEntry(t *testing.T) {
	infractionParams := types.InfractionParameters{
		Downtime: &types.SlashJailParameters{
			SlashFraction: math.LegacyNewDecWithPrec(5, 1),
		},
	}
	k, ctx, ctrl, _, _, providerAddr := setupSweepTest(t, infractionParams)
	defer ctrl.Finish()

	consumerId := uint64(0)
	maturesAt := ctx.BlockTime().Add(time.Hour)
	putPendingDowntimeSlash(t, k, ctx, consumerId, providerAddr, math.NewInt(100), maturesAt, 100)

	// No staking mock expectations set: any call would panic via gomock,
	// verifying the entry is genuinely untouched.
	k.SweepPendingDowntimeSlashes(ctx)

	consAddr := providerAddr.ToSdkConsAddr()
	pending, err := k.PendingDowntimeSlashes.Get(ctx, collections.Join3(consumerId, consAddr.Bytes(), int64(100)))
	require.NoError(t, err)
	require.Equal(t, maturesAt, pending.MaturesAt)
}

// TestSweepPendingDowntimeSlashesCapsFraction asserts that when slashTokens
// is large relative to the validator's slashable stake, the executed
// fraction is capped at InfractionParameters.Downtime.SlashFraction rather
// than the raw (and here, much larger) ratio.
func TestSweepPendingDowntimeSlashesCapsFraction(t *testing.T) {
	slashFractionCap := math.LegacyNewDecWithPrec(5, 1) // 0.5
	infractionParams := types.InfractionParameters{
		Downtime: &types.SlashJailParameters{
			SlashFraction: slashFractionCap,
		},
	}
	k, ctx, ctrl, mocks, validator, providerAddr := setupSweepTest(t, infractionParams)
	defer ctrl.Finish()

	consAddr := providerAddr.ToSdkConsAddr()
	consumerId := uint64(0)
	maturesAt := ctx.BlockTime().Add(-time.Minute)
	// slashTokens (5000) far exceeds totalTokens (1000), raw fraction would be 5.0
	putPendingDowntimeSlash(t, k, ctx, consumerId, providerAddr, math.NewInt(5000), maturesAt, 100)

	powerReduction := math.NewInt(1)
	expectSlashableStakeLookup(t, k, mocks, ctx, validator, consAddr, 1000, powerReduction)

	mocks.MockStakingKeeper.EXPECT().
		SlashWithInfractionReason(ctx, consAddr, int64(0), int64(1000), slashFractionCap, stakingtypes.Infraction_INFRACTION_DOWNTIME).
		Return(math.NewInt(500), nil)

	k.SweepPendingDowntimeSlashes(ctx)

	has, err := k.PendingDowntimeSlashes.Has(ctx, collections.Join3(consumerId, consAddr.Bytes(), int64(100)))
	require.NoError(t, err)
	require.False(t, has)
}

// TestSweepPendingDowntimeSlashesDropsZeroSlashableStake asserts that a
// matured entry whose validator's slashable stake computes to zero tokens
// (e.g. zero last validator power and no non-matured undelegations or
// redelegations) is dropped without calling SlashWithInfractionReason --
// guarding the fraction computation's division by totalTokens against a
// divide-by-zero panic -- and the entry is deleted.
func TestSweepPendingDowntimeSlashesDropsZeroSlashableStake(t *testing.T) {
	infractionParams := types.InfractionParameters{
		Downtime: &types.SlashJailParameters{
			SlashFraction: math.LegacyNewDecWithPrec(5, 1),
		},
	}
	k, ctx, ctrl, mocks, validator, providerAddr := setupSweepTest(t, infractionParams)
	defer ctrl.Finish()

	consAddr := providerAddr.ToSdkConsAddr()
	consumerId := uint64(0)
	maturesAt := ctx.BlockTime().Add(-time.Minute)
	putPendingDowntimeSlash(t, k, ctx, consumerId, providerAddr, math.NewInt(100), maturesAt, 100)

	// lastPower=0 with no undelegations/redelegations makes
	// ComputePowerToSlash (and thus totalTokens) resolve to zero.
	powerReduction := math.NewInt(1)
	expectSlashableStakeLookup(t, k, mocks, ctx, validator, consAddr, 0, powerReduction)

	require.NotPanics(t, func() {
		k.SweepPendingDowntimeSlashes(ctx)
	})

	has, err := k.PendingDowntimeSlashes.Has(ctx, collections.Join3(consumerId, consAddr.Bytes(), int64(100)))
	require.NoError(t, err)
	require.False(t, has, "entry with zero slashable stake should be dropped")
}

// TestSweepPendingDowntimeSlashesDropsOnStakingError asserts that when
// SlashWithInfractionReason itself returns an error, the entry is still
// dropped and deleted (no retry loop), matching the other drop paths.
func TestSweepPendingDowntimeSlashesDropsOnStakingError(t *testing.T) {
	infractionParams := types.InfractionParameters{
		Downtime: &types.SlashJailParameters{
			SlashFraction: math.LegacyNewDecWithPrec(5, 1), // 0.5 cap, does not bind here
		},
	}
	k, ctx, ctrl, mocks, validator, providerAddr := setupSweepTest(t, infractionParams)
	defer ctrl.Finish()

	consAddr := providerAddr.ToSdkConsAddr()
	consumerId := uint64(0)
	maturesAt := ctx.BlockTime().Add(-time.Minute)
	putPendingDowntimeSlash(t, k, ctx, consumerId, providerAddr, math.NewInt(100), maturesAt, 100)

	powerReduction := math.NewInt(1)
	expectSlashableStakeLookup(t, k, mocks, ctx, validator, consAddr, 1000, powerReduction)

	expectedFraction := math.LegacyNewDecWithPrec(1, 1)
	mocks.MockStakingKeeper.EXPECT().
		SlashWithInfractionReason(ctx, consAddr, int64(0), int64(1000), expectedFraction, stakingtypes.Infraction_INFRACTION_DOWNTIME).
		Return(math.ZeroInt(), errors.New("staking slash failed"))

	require.NotPanics(t, func() {
		k.SweepPendingDowntimeSlashes(ctx)
	})

	has, err := k.PendingDowntimeSlashes.Has(ctx, collections.Join3(consumerId, consAddr.Bytes(), int64(100)))
	require.NoError(t, err)
	require.False(t, has, "entry should be deleted even when SlashWithInfractionReason errors")
}

// TestSweepPendingDowntimeSlashesDropsUnbondedValidator asserts that a
// matured entry whose validator has since unbonded is dropped without
// calling SlashWithInfractionReason, the entry is deleted, and the sweep
// does not panic or error.
func TestSweepPendingDowntimeSlashesDropsUnbondedValidator(t *testing.T) {
	infractionParams := types.InfractionParameters{
		Downtime: &types.SlashJailParameters{
			SlashFraction: math.LegacyNewDecWithPrec(5, 1),
		},
	}
	k, ctx, ctrl, mocks, validator, providerAddr := setupSweepTest(t, infractionParams)
	defer ctrl.Finish()
	validator.Status = stakingtypes.Unbonded

	consumerId := uint64(0)
	maturesAt := ctx.BlockTime().Add(-time.Minute)
	putPendingDowntimeSlash(t, k, ctx, consumerId, providerAddr, math.NewInt(100), maturesAt, 100)

	mocks.MockStakingKeeper.EXPECT().
		GetValidatorByConsAddr(ctx, gomock.Any()).
		Return(validator, nil)

	require.NotPanics(t, func() {
		k.SweepPendingDowntimeSlashes(ctx)
	})

	consAddr := providerAddr.ToSdkConsAddr()
	has, err := k.PendingDowntimeSlashes.Has(ctx, collections.Join3(consumerId, consAddr.Bytes(), int64(100)))
	require.NoError(t, err)
	require.False(t, has, "entry for an unbonded validator should be dropped")
}

// TestSweepExpiredWithheldFeeRecordsDeletesExpiredRecords asserts that the
// sweep deletes withheld fee records whose challenge window has elapsed
// (ExpiresAt <= block time), leaving unexpired records untouched. The
// escrowed funds are never moved by this sweep -- they simply stay in the
// consumer's fee pool.
func TestSweepExpiredWithheldFeeRecordsDeletesExpiredRecords(t *testing.T) {
	infractionParams := types.InfractionParameters{
		Downtime: &types.SlashJailParameters{
			SlashFraction: math.LegacyNewDecWithPrec(5, 1),
		},
	}
	k, ctx, ctrl, _, _, _ := setupSweepTest(t, infractionParams)
	defer ctrl.Finish()

	const consumerId uint64 = 0
	expiredAddr := sdk.ConsAddress([]byte("expired_validator___"))
	liveAddr := sdk.ConsAddress([]byte("live_validator______"))

	require.NoError(t, k.WithheldFeeRecords.Set(ctx, collections.Join(consumerId, expiredAddr.Bytes()), types.WithheldFeeRecord{
		ConsumerId:       consumerId,
		ProviderConsAddr: expiredAddr.Bytes(),
		Amount:           sdk.NewInt64Coin("uphoton", 100),
		ExpiresAt:        ctx.BlockTime(), // ExpiresAt <= block time: expired
	}))
	require.NoError(t, k.WithheldFeeRecords.Set(ctx, collections.Join(consumerId, liveAddr.Bytes()), types.WithheldFeeRecord{
		ConsumerId:       consumerId,
		ProviderConsAddr: liveAddr.Bytes(),
		Amount:           sdk.NewInt64Coin("uphoton", 100),
		ExpiresAt:        ctx.BlockTime().Add(time.Hour),
	}))

	k.SweepExpiredWithheldFeeRecords(ctx)

	_, err := k.WithheldFeeRecords.Get(ctx, collections.Join(consumerId, expiredAddr.Bytes()))
	require.Error(t, err, "expired withheld fee record should be deleted")

	_, err = k.WithheldFeeRecords.Get(ctx, collections.Join(consumerId, liveAddr.Bytes()))
	require.NoError(t, err, "unexpired withheld fee record should survive the sweep")
}

// TestSweepPendingDowntimeSlashesExecutesEachDisjointWindowIndependently
// asserts that when two windows are pending for the same (consumer,
// validator) pair -- one per disjoint accepted window -- and both have
// matured, the sweep executes each independently, at its own priced
// slashTokens, and both entries are removed.
func TestSweepPendingDowntimeSlashesExecutesEachDisjointWindowIndependently(t *testing.T) {
	infractionParams := types.InfractionParameters{
		Downtime: &types.SlashJailParameters{
			SlashFraction: math.LegacyNewDecWithPrec(5, 1), // 0.5 cap, does not bind here
		},
	}
	k, ctx, ctrl, mocks, validator, providerAddr := setupSweepTest(t, infractionParams)
	defer ctrl.Finish()

	consAddr := providerAddr.ToSdkConsAddr()
	consumerId := uint64(0)
	maturesAt := ctx.BlockTime().Add(-time.Minute)
	putPendingDowntimeSlash(t, k, ctx, consumerId, providerAddr, math.NewInt(100), maturesAt, 100)
	putPendingDowntimeSlash(t, k, ctx, consumerId, providerAddr, math.NewInt(300), maturesAt, 200)

	powerReduction := math.NewInt(1)
	// One slashable-stake lookup per matured entry.
	expectSlashableStakeLookup(t, k, mocks, ctx, validator, consAddr, 1000, powerReduction)
	expectSlashableStakeLookup(t, k, mocks, ctx, validator, consAddr, 1000, powerReduction)

	mocks.MockStakingKeeper.EXPECT().
		SlashWithInfractionReason(ctx, consAddr, int64(0), int64(1000), math.LegacyNewDecWithPrec(1, 1), stakingtypes.Infraction_INFRACTION_DOWNTIME).
		Return(math.NewInt(100), nil)
	mocks.MockStakingKeeper.EXPECT().
		SlashWithInfractionReason(ctx, consAddr, int64(0), int64(1000), math.LegacyNewDecWithPrec(3, 1), stakingtypes.Infraction_INFRACTION_DOWNTIME).
		Return(math.NewInt(300), nil)

	k.SweepPendingDowntimeSlashes(ctx)

	has, err := k.PendingDowntimeSlashes.Has(ctx, collections.Join3(consumerId, consAddr.Bytes(), int64(100)))
	require.NoError(t, err)
	require.False(t, has, "first window's entry should be executed and deleted")
	has, err = k.PendingDowntimeSlashes.Has(ctx, collections.Join3(consumerId, consAddr.Bytes(), int64(200)))
	require.NoError(t, err)
	require.False(t, has, "second window's entry should be executed and deleted")
}

// TestSweepPendingDowntimeSlashesKeepsWithheldRecordWhilePairStillPending
// asserts the delete-on-last-execute guard's "while any window pends" half:
// when a pair has two pending entries and only one matures, executing it must
// not delete the pair's WithheldFeeRecord, because a window is still
// pending.
func TestSweepPendingDowntimeSlashesKeepsWithheldRecordWhilePairStillPending(t *testing.T) {
	infractionParams := types.InfractionParameters{
		Downtime: &types.SlashJailParameters{
			SlashFraction: math.LegacyNewDecWithPrec(5, 1),
		},
	}
	k, ctx, ctrl, mocks, validator, providerAddr := setupSweepTest(t, infractionParams)
	defer ctrl.Finish()

	consAddr := providerAddr.ToSdkConsAddr()
	consumerId := uint64(0)
	maturedAt := ctx.BlockTime().Add(-time.Minute)
	notYetMaturedAt := ctx.BlockTime().Add(time.Hour)
	putPendingDowntimeSlash(t, k, ctx, consumerId, providerAddr, math.NewInt(100), maturedAt, 100)
	putPendingDowntimeSlash(t, k, ctx, consumerId, providerAddr, math.NewInt(100), notYetMaturedAt, 200)

	require.NoError(t, k.WithheldFeeRecords.Set(ctx, collections.Join(consumerId, consAddr.Bytes()), types.WithheldFeeRecord{
		ConsumerId:       consumerId,
		ProviderConsAddr: consAddr.Bytes(),
		Amount:           sdk.NewInt64Coin("uphoton", 50),
		ExpiresAt:        ctx.BlockTime().Add(time.Hour),
	}))

	powerReduction := math.NewInt(1)
	expectSlashableStakeLookup(t, k, mocks, ctx, validator, consAddr, 1000, powerReduction)
	mocks.MockStakingKeeper.EXPECT().
		SlashWithInfractionReason(ctx, consAddr, int64(0), int64(1000), math.LegacyNewDecWithPrec(1, 1), stakingtypes.Infraction_INFRACTION_DOWNTIME).
		Return(math.NewInt(100), nil)

	k.SweepPendingDowntimeSlashes(ctx)

	has, err := k.PendingDowntimeSlashes.Has(ctx, collections.Join3(consumerId, consAddr.Bytes(), int64(100)))
	require.NoError(t, err)
	require.False(t, has, "matured entry should be executed and deleted")
	has, err = k.PendingDowntimeSlashes.Has(ctx, collections.Join3(consumerId, consAddr.Bytes(), int64(200)))
	require.NoError(t, err)
	require.True(t, has, "unmatured entry for the same pair must survive")

	hasWithheld, err := k.WithheldFeeRecords.Has(ctx, collections.Join(consumerId, consAddr.Bytes()))
	require.NoError(t, err)
	require.True(t, hasWithheld, "withheld fee record must survive while a window is still pending for the pair")
}

// TestSweepPendingDowntimeSlashesDeletesWithheldRecordWhenPairDrainsOnExecute
// asserts the delete-on-last-execute guard's other half: once the sweep
// executes a slash and the pair has no pending entries left, its
// WithheldFeeRecord is deleted (the executed accusations were never
// disproven, so the withheld funds simply stay with the consumer).
func TestSweepPendingDowntimeSlashesDeletesWithheldRecordWhenPairDrainsOnExecute(t *testing.T) {
	infractionParams := types.InfractionParameters{
		Downtime: &types.SlashJailParameters{
			SlashFraction: math.LegacyNewDecWithPrec(5, 1),
		},
	}
	k, ctx, ctrl, mocks, validator, providerAddr := setupSweepTest(t, infractionParams)
	defer ctrl.Finish()

	consAddr := providerAddr.ToSdkConsAddr()
	consumerId := uint64(0)
	maturesAt := ctx.BlockTime().Add(-time.Minute)
	putPendingDowntimeSlash(t, k, ctx, consumerId, providerAddr, math.NewInt(100), maturesAt, 100)

	require.NoError(t, k.WithheldFeeRecords.Set(ctx, collections.Join(consumerId, consAddr.Bytes()), types.WithheldFeeRecord{
		ConsumerId:       consumerId,
		ProviderConsAddr: consAddr.Bytes(),
		Amount:           sdk.NewInt64Coin("uphoton", 50),
		ExpiresAt:        ctx.BlockTime().Add(time.Hour),
	}))

	powerReduction := math.NewInt(1)
	expectSlashableStakeLookup(t, k, mocks, ctx, validator, consAddr, 1000, powerReduction)
	mocks.MockStakingKeeper.EXPECT().
		SlashWithInfractionReason(ctx, consAddr, int64(0), int64(1000), math.LegacyNewDecWithPrec(1, 1), stakingtypes.Infraction_INFRACTION_DOWNTIME).
		Return(math.NewInt(100), nil)

	k.SweepPendingDowntimeSlashes(ctx)

	hasWithheld, err := k.WithheldFeeRecords.Has(ctx, collections.Join(consumerId, consAddr.Bytes()))
	require.NoError(t, err)
	require.False(t, hasWithheld, "withheld fee record must be deleted once the pair has no pending entries left")
}

// TestSweepPendingDowntimeSlashesKeepsWithheldRecordWhenLastEntryOnlyDropped
// asserts that the delete-on-last-execute guard does not fire when a pair's
// last remaining entry is only dropped (never actually executed, e.g. the
// validator has since unbonded): the withheld fee record must survive and
// age out on its own expiry instead.
func TestSweepPendingDowntimeSlashesKeepsWithheldRecordWhenLastEntryOnlyDropped(t *testing.T) {
	infractionParams := types.InfractionParameters{
		Downtime: &types.SlashJailParameters{
			SlashFraction: math.LegacyNewDecWithPrec(5, 1),
		},
	}
	k, ctx, ctrl, mocks, validator, providerAddr := setupSweepTest(t, infractionParams)
	defer ctrl.Finish()
	validator.Status = stakingtypes.Unbonded

	consAddr := providerAddr.ToSdkConsAddr()
	consumerId := uint64(0)
	maturesAt := ctx.BlockTime().Add(-time.Minute)
	putPendingDowntimeSlash(t, k, ctx, consumerId, providerAddr, math.NewInt(100), maturesAt, 100)

	require.NoError(t, k.WithheldFeeRecords.Set(ctx, collections.Join(consumerId, consAddr.Bytes()), types.WithheldFeeRecord{
		ConsumerId:       consumerId,
		ProviderConsAddr: consAddr.Bytes(),
		Amount:           sdk.NewInt64Coin("uphoton", 50),
		ExpiresAt:        ctx.BlockTime().Add(time.Hour),
	}))

	mocks.MockStakingKeeper.EXPECT().
		GetValidatorByConsAddr(ctx, gomock.Any()).
		Return(validator, nil)

	k.SweepPendingDowntimeSlashes(ctx)

	has, err := k.PendingDowntimeSlashes.Has(ctx, collections.Join3(consumerId, consAddr.Bytes(), int64(100)))
	require.NoError(t, err)
	require.False(t, has, "dropped entry should still be removed from the pending queue")

	hasWithheld, err := k.WithheldFeeRecords.Has(ctx, collections.Join(consumerId, consAddr.Bytes()))
	require.NoError(t, err)
	require.True(t, hasWithheld, "a dropped-not-executed last entry must not trigger withheld-record deletion")
}

// TestPruneAcceptedDowntimeWindowsSetsFloorAndRejectsBelowIt drives the full
// retention cycle through the handler and the sweep: an accepted window's
// record is pruned once it is older than DowntimeChallengeWindow +
// DowntimeEvidenceMaxAge, the pair's floor advances to the pruned window
// end, evidence at or below the floor is rejected with the floor error, and
// a fresh window above the floor is accepted.
func TestPruneAcceptedDowntimeWindowsSetsFloorAndRejectsBelowIt(t *testing.T) {
	windowEndTime := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	spawnTime := windowEndTime.Add(-30 * 24 * time.Hour)
	infractionParams := downtimeParams(8, "0.5", 0, 7*24*time.Hour, 72*time.Hour)
	horizon := infractionParams.DowntimeChallengeWindow + infractionParams.DowntimeEvidenceMaxAge

	k, ctx, ctrl, mocks, consumerId, providerAddr := setupDowntimeTest(t, infractionParams, spawnTime, windowEndTime)
	defer ctrl.Finish()
	ctx = ctx.WithBlockTime(windowEndTime)
	consAddr := providerAddr.ToSdkConsAddr()

	k.SetEpochShareRecord(ctx, consumerId, windowEndTime, math.NewInt(1000))
	mocks.MockPhotonKeeper.EXPECT().ConversionRate(gomock.Any()).Return(math.LegacyNewDec(2), nil).AnyTimes()

	// Accept the window [93, 100], then simulate its slash maturing and
	// executing (the sweep removes the pending entry; the accepted record
	// stays).
	packet := vaastypes.NewEvidencePacketData(sdk.ConsAddress(consAddr), 93, []byte{0x3F}, 8, 8, math.LegacyMustNewDecFromStr("0.5"))
	require.NoError(t, k.HandleConsumerEvidencePacket(ctx, consumerId, packet))
	acceptedKey := collections.Join3(consumerId, consAddr.Bytes(), int64(100))
	require.NoError(t, k.PendingDowntimeSlashes.Remove(ctx, acceptedKey))

	// One block inside the retention horizon the record survives pruning.
	almostCtx := ctx.WithBlockTime(windowEndTime.Add(horizon))
	k.PruneAcceptedDowntimeWindows(almostCtx, infractionParams)
	has, err := k.AcceptedDowntimeWindows.Has(almostCtx, acceptedKey)
	require.NoError(t, err)
	require.True(t, has, "record inside the retention horizon must not be pruned")

	// Past the horizon the record is pruned and the floor advances to the
	// pruned window end.
	prunedCtx := ctx.WithBlockTime(windowEndTime.Add(horizon + time.Minute))
	k.PruneAcceptedDowntimeWindows(prunedCtx, infractionParams)
	has, err = k.AcceptedDowntimeWindows.Has(prunedCtx, acceptedKey)
	require.NoError(t, err)
	require.False(t, has, "record past the retention horizon must be pruned")
	pairKey := collections.Join(consumerId, consAddr.Bytes())
	floor, err := k.DowntimeWindowFloors.Get(prunedCtx, pairKey)
	require.NoError(t, err)
	require.Equal(t, int64(100), floor)

	// Anchor subsequent evidence windows to the current block time so they
	// pass the evidence-age check and reach the floor check.
	freshWindowEnd := prunedCtx.BlockTime()
	k.OverrideWindowEndTimestampForTest(func(sdk.Context, string, int64) (time.Time, error) {
		return freshWindowEnd, nil
	})

	// A window starting inside the pruned range ([95, 102]) and one starting
	// exactly at the floor ([100, 107]) are both rejected with the floor
	// error.
	belowFloor := vaastypes.NewEvidencePacketData(sdk.ConsAddress(consAddr), 95, []byte{0x3F}, 8, 8, math.LegacyMustNewDecFromStr("0.5"))
	err = k.HandleConsumerEvidencePacket(prunedCtx, consumerId, belowFloor)
	require.Error(t, err)
	require.Contains(t, err.Error(), "pruned acceptance floor")

	atFloor := vaastypes.NewEvidencePacketData(sdk.ConsAddress(consAddr), 100, []byte{0x3F}, 8, 8, math.LegacyMustNewDecFromStr("0.5"))
	err = k.HandleConsumerEvidencePacket(prunedCtx, consumerId, atFloor)
	require.Error(t, err)
	require.Contains(t, err.Error(), "pruned acceptance floor")

	// A fresh window above the floor ([101, 108]) is accepted.
	k.SetEpochShareRecord(prunedCtx, consumerId, freshWindowEnd, math.NewInt(1000))
	fresh := vaastypes.NewEvidencePacketData(sdk.ConsAddress(consAddr), 101, []byte{0x3F}, 8, 8, math.LegacyMustNewDecFromStr("0.5"))
	require.NoError(t, k.HandleConsumerEvidencePacket(prunedCtx, consumerId, fresh))
	freshPending, err := k.PendingDowntimeSlashes.Get(prunedCtx, collections.Join3(consumerId, consAddr.Bytes(), int64(108)))
	require.NoError(t, err)
	require.Equal(t, int64(101), freshPending.WindowStartHeight)
}

// TestPruneAcceptedDowntimeWindowsScopedToExpiredRecords asserts pruning
// isolation: only records past the retention horizon are pruned, the floor
// advances to the highest pruned window end per pair, and other pairs' and
// consumers' records and floors are untouched.
func TestPruneAcceptedDowntimeWindowsScopedToExpiredRecords(t *testing.T) {
	infractionParams := downtimeParams(8, "0.5", 0, 7*24*time.Hour, 72*time.Hour)
	horizon := infractionParams.DowntimeChallengeWindow + infractionParams.DowntimeEvidenceMaxAge

	k, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()
	now := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	ctx = ctx.WithBlockTime(now)
	k.SetInfractionParams(ctx, infractionParams)

	addrA := []byte("provider-cons-addr-aaaa1")
	addrB := []byte("provider-cons-addr-bbbb2")
	oldAcceptedAt := now.Add(-horizon - time.Minute)

	// Pair (0, addrA): two expired records; the floor must land on the
	// highest pruned window end.
	require.NoError(t, k.AcceptedDowntimeWindows.Set(ctx, collections.Join3(uint64(0), addrA, int64(50)),
		types.AcceptedDowntimeWindow{WindowStart: 43, AcceptedAt: oldAcceptedAt}))
	require.NoError(t, k.AcceptedDowntimeWindows.Set(ctx, collections.Join3(uint64(0), addrA, int64(100)),
		types.AcceptedDowntimeWindow{WindowStart: 93, AcceptedAt: oldAcceptedAt}))
	// Pair (0, addrB): a fresh record that must survive with no floor.
	require.NoError(t, k.AcceptedDowntimeWindows.Set(ctx, collections.Join3(uint64(0), addrB, int64(200)),
		types.AcceptedDowntimeWindow{WindowStart: 193, AcceptedAt: now}))
	// Pair (1, addrA): an expired record on another consumer; pruned into
	// that consumer's own floor.
	require.NoError(t, k.AcceptedDowntimeWindows.Set(ctx, collections.Join3(uint64(1), addrA, int64(300)),
		types.AcceptedDowntimeWindow{WindowStart: 293, AcceptedAt: oldAcceptedAt}))

	k.PruneAcceptedDowntimeWindows(ctx, infractionParams)

	for _, end := range []int64{50, 100} {
		has, err := k.AcceptedDowntimeWindows.Has(ctx, collections.Join3(uint64(0), addrA, end))
		require.NoError(t, err)
		require.False(t, has, "expired record must be pruned")
	}
	floorA, err := k.DowntimeWindowFloors.Get(ctx, collections.Join(uint64(0), addrA))
	require.NoError(t, err)
	require.Equal(t, int64(100), floorA, "floor must advance to the highest pruned window end")

	hasB, err := k.AcceptedDowntimeWindows.Has(ctx, collections.Join3(uint64(0), addrB, int64(200)))
	require.NoError(t, err)
	require.True(t, hasB, "fresh record on another pair must survive")
	_, err = k.DowntimeWindowFloors.Get(ctx, collections.Join(uint64(0), addrB))
	require.ErrorIs(t, err, collections.ErrNotFound, "pair with no pruned record must have no floor")

	hasC, err := k.AcceptedDowntimeWindows.Has(ctx, collections.Join3(uint64(1), addrA, int64(300)))
	require.NoError(t, err)
	require.False(t, hasC)
	floorC, err := k.DowntimeWindowFloors.Get(ctx, collections.Join(uint64(1), addrA))
	require.NoError(t, err)
	require.Equal(t, int64(300), floorC, "another consumer's pruning must land on its own floor")
}

// TestPruneAcceptedDowntimeWindowsKeepsRecordWhilePendingSlashMatures pins
// the retention invariant that a pending downtime slash always has its
// accepted record: a governance shrink of the retention horizon
// (DowntimeChallengeWindow + DowntimeEvidenceMaxAge) below the pending
// slash's original challenge window must not prune the record while the
// slash is still maturing -- otherwise the chain's own exported genesis
// fails GenesisState.Validate. Once the slash executes, the record is
// pruned, the floor advances, and the export still validates.
func TestPruneAcceptedDowntimeWindowsKeepsRecordWhilePendingSlashMatures(t *testing.T) {
	windowEndTime := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	spawnTime := windowEndTime.Add(-30 * 24 * time.Hour)
	infractionParams := downtimeParams(8, "0.5", 0, 7*24*time.Hour, 72*time.Hour)
	// Slash-jail params so the sweep can execute the matured slash and the
	// exported InfractionParameters pass GenesisState.Validate.
	infractionParams.DoubleSign = &types.SlashJailParameters{
		JailDuration:  1200 * time.Second,
		SlashFraction: math.LegacyNewDecWithPrec(5, 1),
		Tombstone:     true,
	}
	infractionParams.Downtime = &types.SlashJailParameters{
		SlashFraction: math.LegacyNewDecWithPrec(5, 1),
	}

	k, ctx, ctrl, mocks, consumerId, providerAddr := setupDowntimeTest(t, infractionParams, spawnTime, windowEndTime)
	defer ctrl.Finish()
	ctx = ctx.WithBlockTime(windowEndTime)
	consAddr := providerAddr.ToSdkConsAddr()

	// Make the consumer a fully exportable LAUNCHED chain so the exported
	// genesis can be validated at each step.
	require.Equal(t, consumerId, k.FetchAndIncrementConsumerId(ctx))
	k.SetConsumerOwnerAddress(ctx, consumerId, sdk.AccAddress([]byte("vaas-test-owner-1234")).String())
	cg := *vaastypes.DefaultConsumerGenesisState()
	cg.NewChain = true
	require.NoError(t, k.SetConsumerGenesis(ctx, consumerId, cg))
	k.SetParams(ctx, types.DefaultParams())
	k.SetValidatorSetUpdateId(ctx, 1)

	// Accept the window [93, 100]: the pending slash is queued (maturing at
	// windowEndTime + 7d) and the accepted record is written.
	k.SetEpochShareRecord(ctx, consumerId, windowEndTime, math.NewInt(1000))
	mocks.MockPhotonKeeper.EXPECT().ConversionRate(ctx).Return(math.LegacyNewDec(2), nil)
	packet := vaastypes.NewEvidencePacketData(sdk.ConsAddress(consAddr), 93, []byte{0x3F}, 8, 8, math.LegacyMustNewDecFromStr("0.5"))
	require.NoError(t, k.HandleConsumerEvidencePacket(ctx, consumerId, packet))
	key := collections.Join3(consumerId, consAddr.Bytes(), int64(100))
	pairKey := collections.Join(consumerId, consAddr.Bytes())

	// Governance shrinks the retention horizon far below the pending slash's
	// original 7-day challenge window.
	shrunk := infractionParams
	shrunk.DowntimeChallengeWindow = time.Hour
	shrunk.DowntimeEvidenceMaxAge = time.Hour
	k.SetInfractionParams(ctx, shrunk)

	// Past the shrunk horizon (2h) but before the pending slash matures: the
	// record and the floor stay untouched and the exported genesis validates.
	shrunkCtx := ctx.WithBlockTime(windowEndTime.Add(3 * time.Hour))
	k.SweepPendingDowntimeSlashes(shrunkCtx)
	k.PruneAcceptedDowntimeWindows(shrunkCtx, shrunk)
	has, err := k.AcceptedDowntimeWindows.Has(shrunkCtx, key)
	require.NoError(t, err)
	require.True(t, has, "record with a still-pending slash must not be pruned")
	_, err = k.DowntimeWindowFloors.Get(shrunkCtx, pairKey)
	require.ErrorIs(t, err, collections.ErrNotFound, "floor must not advance while the slash is pending")
	hasPending, err := k.PendingDowntimeSlashes.Has(shrunkCtx, key)
	require.NoError(t, err)
	require.True(t, hasPending, "pending slash must still be maturing")
	require.NoError(t, k.ExportGenesis(shrunkCtx).Validate())

	// Once the slash matures and executes, the prune that follows the sweep
	// (mirroring module.go's BeginBlock sequence) removes the record and
	// advances the floor; the exported genesis still validates.
	maturedCtx := ctx.WithBlockTime(windowEndTime.Add(7*24*time.Hour + time.Minute))
	pubKey, err := cryptocodec.FromCmtPubKeyInterface(tmtypes.NewMockPV().PrivKey.PubKey())
	require.NoError(t, err)
	validator, err := stakingtypes.NewValidator(
		sdk.ValAddress(pubKey.Address()).String(),
		pubKey,
		stakingtypes.NewDescription("", "", "", "", ""),
	)
	require.NoError(t, err)
	validator.Status = stakingtypes.Bonded
	valConsAddr, err := validator.GetConsAddr()
	require.NoError(t, err)
	expectSlashableStakeLookup(t, k, mocks, maturedCtx, validator, consAddr, 1000, math.NewInt(1))
	// slashTokens = 1000 * (6/8) / 2 = 375; totalTokens = 1000 => 0.375
	mocks.MockStakingKeeper.EXPECT().
		SlashWithInfractionReason(maturedCtx, sdk.ConsAddress(valConsAddr), int64(0), int64(1000), math.LegacyNewDecWithPrec(375, 3), stakingtypes.Infraction_INFRACTION_DOWNTIME).
		Return(math.NewInt(375), nil)
	k.SweepPendingDowntimeSlashes(maturedCtx)
	k.PruneAcceptedDowntimeWindows(maturedCtx, shrunk)

	has, err = k.AcceptedDowntimeWindows.Has(maturedCtx, key)
	require.NoError(t, err)
	require.False(t, has, "record must be pruned once its slash has executed")
	floor, err := k.DowntimeWindowFloors.Get(maturedCtx, pairKey)
	require.NoError(t, err)
	require.Equal(t, int64(100), floor)
	require.NoError(t, k.ExportGenesis(maturedCtx).Validate())
}
