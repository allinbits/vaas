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
// providerAddr) maturing at maturesAt with the given slashTokens.
func putPendingDowntimeSlash(
	t *testing.T,
	k providerkeeper.Keeper,
	ctx sdk.Context,
	consumerId uint64,
	providerAddr types.ProviderConsAddress,
	slashTokens math.Int,
	maturesAt time.Time,
) {
	t.Helper()
	key := collections.Join(consumerId, providerAddr.ToSdkConsAddr().Bytes())
	require.NoError(t, k.PendingDowntimeSlashes.Set(ctx, key, types.PendingDowntimeSlash{
		ConsumerId:       consumerId,
		ProviderConsAddr: providerAddr.ToSdkConsAddr().Bytes(),
		SlashTokens:      slashTokens,
		MaturesAt:        maturesAt,
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
	putPendingDowntimeSlash(t, k, ctx, consumerId, providerAddr, math.NewInt(100), maturesAt)

	powerReduction := math.NewInt(1)
	expectSlashableStakeLookup(t, k, mocks, ctx, validator, consAddr, 1000, powerReduction)

	// totalTokens = 1000 * 1 = 1000; fraction = 100/1000 = 0.1
	expectedFraction := math.LegacyNewDecWithPrec(1, 1)
	mocks.MockStakingKeeper.EXPECT().
		SlashWithInfractionReason(ctx, consAddr, int64(0), int64(1000), expectedFraction, stakingtypes.Infraction_INFRACTION_DOWNTIME).
		Return(math.NewInt(100), nil)

	k.SweepPendingDowntimeSlashes(ctx)

	has, err := k.PendingDowntimeSlashes.Has(ctx, collections.Join(consumerId, consAddr.Bytes()))
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
	putPendingDowntimeSlash(t, k, ctx, consumerId, providerAddr, math.NewInt(100), maturesAt)

	// No staking mock expectations set: any call would panic via gomock,
	// verifying the entry is genuinely untouched.
	k.SweepPendingDowntimeSlashes(ctx)

	consAddr := providerAddr.ToSdkConsAddr()
	pending, err := k.PendingDowntimeSlashes.Get(ctx, collections.Join(consumerId, consAddr.Bytes()))
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
	putPendingDowntimeSlash(t, k, ctx, consumerId, providerAddr, math.NewInt(5000), maturesAt)

	powerReduction := math.NewInt(1)
	expectSlashableStakeLookup(t, k, mocks, ctx, validator, consAddr, 1000, powerReduction)

	mocks.MockStakingKeeper.EXPECT().
		SlashWithInfractionReason(ctx, consAddr, int64(0), int64(1000), slashFractionCap, stakingtypes.Infraction_INFRACTION_DOWNTIME).
		Return(math.NewInt(500), nil)

	k.SweepPendingDowntimeSlashes(ctx)

	has, err := k.PendingDowntimeSlashes.Has(ctx, collections.Join(consumerId, consAddr.Bytes()))
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
	putPendingDowntimeSlash(t, k, ctx, consumerId, providerAddr, math.NewInt(100), maturesAt)

	// lastPower=0 with no undelegations/redelegations makes
	// ComputePowerToSlash (and thus totalTokens) resolve to zero.
	powerReduction := math.NewInt(1)
	expectSlashableStakeLookup(t, k, mocks, ctx, validator, consAddr, 0, powerReduction)

	require.NotPanics(t, func() {
		k.SweepPendingDowntimeSlashes(ctx)
	})

	has, err := k.PendingDowntimeSlashes.Has(ctx, collections.Join(consumerId, consAddr.Bytes()))
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
	putPendingDowntimeSlash(t, k, ctx, consumerId, providerAddr, math.NewInt(100), maturesAt)

	powerReduction := math.NewInt(1)
	expectSlashableStakeLookup(t, k, mocks, ctx, validator, consAddr, 1000, powerReduction)

	expectedFraction := math.LegacyNewDecWithPrec(1, 1)
	mocks.MockStakingKeeper.EXPECT().
		SlashWithInfractionReason(ctx, consAddr, int64(0), int64(1000), expectedFraction, stakingtypes.Infraction_INFRACTION_DOWNTIME).
		Return(math.ZeroInt(), errors.New("staking slash failed"))

	require.NotPanics(t, func() {
		k.SweepPendingDowntimeSlashes(ctx)
	})

	has, err := k.PendingDowntimeSlashes.Has(ctx, collections.Join(consumerId, consAddr.Bytes()))
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
	putPendingDowntimeSlash(t, k, ctx, consumerId, providerAddr, math.NewInt(100), maturesAt)

	mocks.MockStakingKeeper.EXPECT().
		GetValidatorByConsAddr(ctx, gomock.Any()).
		Return(validator, nil)

	require.NotPanics(t, func() {
		k.SweepPendingDowntimeSlashes(ctx)
	})

	consAddr := providerAddr.ToSdkConsAddr()
	has, err := k.PendingDowntimeSlashes.Has(ctx, collections.Join(consumerId, consAddr.Bytes()))
	require.NoError(t, err)
	require.False(t, has, "entry for an unbonded validator should be dropped")
}
