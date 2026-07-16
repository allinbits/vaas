package keeper_test

import (
	"errors"
	"testing"
	"time"

	"cosmossdk.io/collections"
	addresscodec "cosmossdk.io/core/address"
	"cosmossdk.io/math"
	testkeeper "github.com/allinbits/vaas/testutil/keeper"
	providertypes "github.com/allinbits/vaas/x/vaas/provider/types"
	"github.com/cosmos/cosmos-sdk/codec/address"
	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	sdk "github.com/cosmos/cosmos-sdk/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

const epochMultiplier = providertypes.DefaultBlocksPerEpoch // 600

func newBondedValidator(t *testing.T, codec addresscodec.Codec, opSeed byte) (stakingtypes.Validator, []byte) {
	t.Helper()
	opBytes := make([]byte, 20)
	for i := range opBytes {
		opBytes[i] = opSeed
	}
	op, err := codec.BytesToString(opBytes)
	require.NoError(t, err)
	pk := ed25519.GenPrivKey().PubKey()
	val, err := stakingtypes.NewValidator(op, pk, stakingtypes.Description{})
	require.NoError(t, err)
	val.Status = stakingtypes.Bonded
	val.Tokens = sdk.DefaultPowerReduction
	val.DelegatorShares = math.LegacyNewDecFromInt(sdk.DefaultPowerReduction)
	return val, opBytes
}

// accAddr converts raw operator bytes to an account-prefixed bech32 string.
func accAddr(opBytes []byte) string {
	return sdk.AccAddress(opBytes).String()
}

// TestDistributeConsumerFees splits each consumer's fees directly to bonded
// validators via a single InputOutputCoins call.
func TestDistributeConsumerFees(t *testing.T) {
	params := testkeeper.NewInMemKeeperParams(t)
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, params)
	defer ctrl.Finish()

	valAddrCodec := address.NewBech32Codec("cosmosvaloper")
	mocks.MockStakingKeeper.EXPECT().ValidatorAddressCodec().Return(valAddrCodec).AnyTimes()

	val1, val1Bytes := newBondedValidator(t, valAddrCodec, 1)
	val1.Tokens = sdk.DefaultPowerReduction.MulRaw(10)
	val2, val2Bytes := newBondedValidator(t, valAddrCodec, 2)
	val2.Tokens = sdk.DefaultPowerReduction.MulRaw(20)

	feesPerBlock := sdk.NewInt64Coin("uphoton", 10)
	feesPerEpoch := sdk.NewCoin("uphoton", feesPerBlock.Amount.MulRaw(epochMultiplier))
	share := feesPerEpoch.Amount.QuoRaw(2) // 3000
	shareCoins := sdk.NewCoins(sdk.NewCoin("uphoton", share))

	consumer0 := k.FetchAndIncrementConsumerId(ctx)
	consumer1 := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, consumer0, providertypes.CONSUMER_PHASE_LAUNCHED)
	k.SetConsumerPhase(ctx, consumer1, providertypes.CONSUMER_PHASE_LAUNCHED)

	providerParams := providertypes.DefaultParams()
	providerParams.FeesPerBlockAmount = feesPerBlock.Amount
	k.SetParams(ctx, providerParams)

	consumer0Pool := k.GetConsumerFeePoolAddress(consumer0)
	consumer1Pool := k.GetConsumerFeePoolAddress(consumer1)

	mocks.MockStakingKeeper.EXPECT().
		GetBondedValidatorsByPower(gomock.Any()).
		Return([]stakingtypes.Validator{val1, val2}, nil)

	// consumer0
	mocks.MockBankKeeper.EXPECT().
		GetBalance(gomock.Any(), consumer0Pool, "uphoton").
		Return(feesPerEpoch)
	mocks.MockBankKeeper.EXPECT().
		InputOutputCoins(gomock.Any(),
			banktypes.Input{Address: consumer0Pool.String(), Coins: sdk.NewCoins(sdk.NewCoin("uphoton", share.MulRaw(2)))},
			[]banktypes.Output{
				{Address: accAddr(val1Bytes), Coins: shareCoins},
				{Address: accAddr(val2Bytes), Coins: shareCoins},
			},
		).Return(nil)

	// consumer1
	mocks.MockBankKeeper.EXPECT().
		GetBalance(gomock.Any(), consumer1Pool, "uphoton").
		Return(feesPerEpoch)
	mocks.MockBankKeeper.EXPECT().
		InputOutputCoins(gomock.Any(),
			banktypes.Input{Address: consumer1Pool.String(), Coins: sdk.NewCoins(sdk.NewCoin("uphoton", share.MulRaw(2)))},
			[]banktypes.Output{
				{Address: accAddr(val1Bytes), Coins: shareCoins},
				{Address: accAddr(val2Bytes), Coins: shareCoins},
			},
		).Return(nil)

	require.NoError(t, k.DistributeConsumerFees(ctx))
	require.False(t, k.IsConsumerInDebt(ctx, consumer0))
	require.False(t, k.IsConsumerInDebt(ctx, consumer1))
}

// TestDistributeConsumerFeesSkipsUnderfunded: insufficient balance → all
// validators skipped, consumer marked in debt.
func TestDistributeConsumerFeesSkipsUnderfunded(t *testing.T) {
	params := testkeeper.NewInMemKeeperParams(t)
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, params)
	defer ctrl.Finish()

	valAddrCodec := address.NewBech32Codec("cosmosvaloper")
	mocks.MockStakingKeeper.EXPECT().ValidatorAddressCodec().Return(valAddrCodec).AnyTimes()

	val1, _ := newBondedValidator(t, valAddrCodec, 1)
	val2, _ := newBondedValidator(t, valAddrCodec, 2)

	feesPerBlock := sdk.NewInt64Coin("uphoton", 10)
	feesPerEpoch := sdk.NewCoin("uphoton", feesPerBlock.Amount.MulRaw(epochMultiplier))

	consumer0 := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, consumer0, providertypes.CONSUMER_PHASE_LAUNCHED)

	providerParams := providertypes.DefaultParams()
	providerParams.FeesPerBlockAmount = feesPerBlock.Amount
	k.SetParams(ctx, providerParams)

	consumer0Pool := k.GetConsumerFeePoolAddress(consumer0)

	mocks.MockStakingKeeper.EXPECT().
		GetBondedValidatorsByPower(gomock.Any()).
		Return([]stakingtypes.Validator{val1, val2}, nil)

	// Balance too low → in debt, no InputOutputCoins
	mocks.MockBankKeeper.EXPECT().
		GetBalance(gomock.Any(), consumer0Pool, "uphoton").
		Return(sdk.NewCoin("uphoton", feesPerEpoch.Amount.QuoRaw(2)))

	require.NoError(t, k.DistributeConsumerFees(ctx))
	require.True(t, k.IsConsumerInDebt(ctx, consumer0))
}

// TestDistributeConsumerFeesClearsDebtWhenRecovered: a consumer previously in
// debt pays successfully and the flag is cleared.
func TestDistributeConsumerFeesClearsDebtWhenRecovered(t *testing.T) {
	params := testkeeper.NewInMemKeeperParams(t)
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, params)
	defer ctrl.Finish()

	valAddrCodec := address.NewBech32Codec("cosmosvaloper")
	mocks.MockStakingKeeper.EXPECT().ValidatorAddressCodec().Return(valAddrCodec).AnyTimes()

	val1, val1Bytes := newBondedValidator(t, valAddrCodec, 1)
	val2, val2Bytes := newBondedValidator(t, valAddrCodec, 2)

	consumer0 := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, consumer0, providertypes.CONSUMER_PHASE_LAUNCHED)
	k.SetConsumerInDebt(ctx, consumer0, true)

	feesPerBlock := sdk.NewInt64Coin("uphoton", 10)
	feesPerEpoch := sdk.NewCoin("uphoton", feesPerBlock.Amount.MulRaw(epochMultiplier))
	share := feesPerEpoch.Amount.QuoRaw(2)
	shareCoins := sdk.NewCoins(sdk.NewCoin("uphoton", share))

	providerParams := providertypes.DefaultParams()
	providerParams.FeesPerBlockAmount = feesPerBlock.Amount
	k.SetParams(ctx, providerParams)

	consumer0Pool := k.GetConsumerFeePoolAddress(consumer0)

	mocks.MockStakingKeeper.EXPECT().
		GetBondedValidatorsByPower(gomock.Any()).
		Return([]stakingtypes.Validator{val1, val2}, nil)
	mocks.MockBankKeeper.EXPECT().
		GetBalance(gomock.Any(), consumer0Pool, "uphoton").
		Return(feesPerEpoch)
	mocks.MockBankKeeper.EXPECT().
		InputOutputCoins(gomock.Any(),
			banktypes.Input{Address: consumer0Pool.String(), Coins: sdk.NewCoins(sdk.NewCoin("uphoton", share.MulRaw(2)))},
			[]banktypes.Output{
				{Address: accAddr(val1Bytes), Coins: shareCoins},
				{Address: accAddr(val2Bytes), Coins: shareCoins},
			},
		).Return(nil)

	require.NoError(t, k.DistributeConsumerFees(ctx))
	require.False(t, k.IsConsumerInDebt(ctx, consumer0))
}

// TestDistributeConsumerFeesContinuesOnGenericError: InputOutputCoins fails
// with a non-insufficient-funds error on one consumer — logged, debt flag
// unchanged. A second consumer in the same call distributes normally,
// proving one consumer's error does not block the others.
func TestDistributeConsumerFeesContinuesOnGenericError(t *testing.T) {
	params := testkeeper.NewInMemKeeperParams(t)
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, params)
	defer ctrl.Finish()

	valAddrCodec := address.NewBech32Codec("cosmosvaloper")
	mocks.MockStakingKeeper.EXPECT().ValidatorAddressCodec().Return(valAddrCodec).AnyTimes()

	val1, val1Bytes := newBondedValidator(t, valAddrCodec, 1)
	val2, val2Bytes := newBondedValidator(t, valAddrCodec, 2)

	feesPerBlock := sdk.NewInt64Coin("uphoton", 10)
	feesPerEpoch := sdk.NewCoin("uphoton", feesPerBlock.Amount.MulRaw(epochMultiplier))
	share := feesPerEpoch.Amount.QuoRaw(2)
	shareCoins := sdk.NewCoins(sdk.NewCoin("uphoton", share))

	consumer0 := k.FetchAndIncrementConsumerId(ctx)
	consumer1 := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, consumer0, providertypes.CONSUMER_PHASE_LAUNCHED)
	k.SetConsumerPhase(ctx, consumer1, providertypes.CONSUMER_PHASE_LAUNCHED)

	providerParams := providertypes.DefaultParams()
	providerParams.FeesPerBlockAmount = feesPerBlock.Amount
	k.SetParams(ctx, providerParams)

	consumer0Pool := k.GetConsumerFeePoolAddress(consumer0)
	consumer1Pool := k.GetConsumerFeePoolAddress(consumer1)

	mocks.MockStakingKeeper.EXPECT().
		GetBondedValidatorsByPower(gomock.Any()).
		Return([]stakingtypes.Validator{val1, val2}, nil)

	// consumer0: InputOutputCoins fails with a generic error.
	mocks.MockBankKeeper.EXPECT().
		GetBalance(gomock.Any(), consumer0Pool, "uphoton").
		Return(feesPerEpoch)
	mocks.MockBankKeeper.EXPECT().
		InputOutputCoins(gomock.Any(),
			banktypes.Input{Address: consumer0Pool.String(), Coins: sdk.NewCoins(sdk.NewCoin("uphoton", share.MulRaw(2)))},
			[]banktypes.Output{
				{Address: accAddr(val1Bytes), Coins: shareCoins},
				{Address: accAddr(val2Bytes), Coins: shareCoins},
			},
		).Return(errors.New("bank send restriction"))

	// consumer1: distribution succeeds despite consumer0's failure.
	mocks.MockBankKeeper.EXPECT().
		GetBalance(gomock.Any(), consumer1Pool, "uphoton").
		Return(feesPerEpoch)
	mocks.MockBankKeeper.EXPECT().
		InputOutputCoins(gomock.Any(),
			banktypes.Input{Address: consumer1Pool.String(), Coins: sdk.NewCoins(sdk.NewCoin("uphoton", share.MulRaw(2)))},
			[]banktypes.Output{
				{Address: accAddr(val1Bytes), Coins: shareCoins},
				{Address: accAddr(val2Bytes), Coins: shareCoins},
			},
		).Return(nil)

	require.NoError(t, k.DistributeConsumerFees(ctx))
	require.False(t, k.IsConsumerInDebt(ctx, consumer0))
	require.False(t, k.IsConsumerInDebt(ctx, consumer1))
}

// TestDistributeConsumerFeesNoBondedValidators: no bonded validators → nothing sent.
func TestDistributeConsumerFeesNoBondedValidators(t *testing.T) {
	params := testkeeper.NewInMemKeeperParams(t)
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, params)
	defer ctrl.Finish()

	mocks.MockStakingKeeper.EXPECT().
		GetBondedValidatorsByPower(gomock.Any()).
		Return(nil, nil)

	require.NoError(t, k.DistributeConsumerFees(ctx))
}

// TestDistributeConsumerFeesSkipsNonLaunched: only LAUNCHED consumers are charged.
func TestDistributeConsumerFeesSkipsNonLaunched(t *testing.T) {
	params := testkeeper.NewInMemKeeperParams(t)
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, params)
	defer ctrl.Finish()

	valAddrCodec := address.NewBech32Codec("cosmosvaloper")
	mocks.MockStakingKeeper.EXPECT().ValidatorAddressCodec().Return(valAddrCodec).AnyTimes()

	val1, _ := newBondedValidator(t, valAddrCodec, 1)

	consumer0 := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, consumer0, providertypes.CONSUMER_PHASE_REGISTERED)

	providerParams := providertypes.DefaultParams()
	providerParams.FeesPerBlockAmount = math.NewInt(10)
	k.SetParams(ctx, providerParams)

	mocks.MockStakingKeeper.EXPECT().
		GetBondedValidatorsByPower(gomock.Any()).
		Return([]stakingtypes.Validator{val1}, nil)

	// No GetBalance/InputOutputCoins expected — consumer0 is REGISTERED.
	require.NoError(t, k.DistributeConsumerFees(ctx))
}

// TestDistributeConsumerFeesExcludesDowntime: validators with epoch downtime
// are excluded from outputs. Their share stays in the consumer pool.
func TestDistributeConsumerFeesExcludesDowntime(t *testing.T) {
	params := testkeeper.NewInMemKeeperParams(t)
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, params)
	defer ctrl.Finish()

	valAddrCodec := address.NewBech32Codec("cosmosvaloper")
	mocks.MockStakingKeeper.EXPECT().ValidatorAddressCodec().Return(valAddrCodec).AnyTimes()

	val1, val1Bytes := newBondedValidator(t, valAddrCodec, 1)
	val2, _ := newBondedValidator(t, valAddrCodec, 2)

	consAddr1, err := val1.GetConsAddr()
	require.NoError(t, err)
	consAddr2, err := val2.GetConsAddr()
	require.NoError(t, err)

	feesPerBlock := sdk.NewInt64Coin("uphoton", 10)
	feesPerEpoch := sdk.NewCoin("uphoton", feesPerBlock.Amount.MulRaw(epochMultiplier))
	share := feesPerEpoch.Amount.QuoRaw(2) // share = total / num_bonded (not eligible)
	shareCoins := sdk.NewCoins(sdk.NewCoin("uphoton", share))

	consumer0 := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, consumer0, providertypes.CONSUMER_PHASE_LAUNCHED)
	k.MarkEpochDowntime(ctx, consumer0, consAddr2)

	providerParams := providertypes.DefaultParams()
	providerParams.FeesPerBlockAmount = math.NewInt(10)
	k.SetParams(ctx, providerParams)

	consumer0Pool := k.GetConsumerFeePoolAddress(consumer0)

	mocks.MockStakingKeeper.EXPECT().
		GetBondedValidatorsByPower(gomock.Any()).
		Return([]stakingtypes.Validator{val1, val2}, nil)

	mocks.MockBankKeeper.EXPECT().
		GetBalance(gomock.Any(), consumer0Pool, "uphoton").
		Return(feesPerEpoch)

	// Only val1 in outputs. Input is share (not share*2) since only 1 eligible.
	mocks.MockBankKeeper.EXPECT().
		InputOutputCoins(gomock.Any(),
			banktypes.Input{Address: consumer0Pool.String(), Coins: sdk.NewCoins(sdk.NewCoin("uphoton", share))},
			[]banktypes.Output{
				{Address: accAddr(val1Bytes), Coins: shareCoins},
			},
		).Return(nil)

	require.False(t, k.IsEpochDowntime(ctx, consumer0, consAddr1))
	require.True(t, k.IsEpochDowntime(ctx, consumer0, consAddr2))
	require.NoError(t, k.DistributeConsumerFees(ctx))
	require.False(t, k.IsConsumerInDebt(ctx, consumer0))
}

// TestEpochDowntimeTracking tests the lifecycle: mark, check, clear.
// Downtime is tracked per consumer, so the same validator can be flagged on
// one consumer but not another.
func TestEpochDowntimeTracking(t *testing.T) {
	params := testkeeper.NewInMemKeeperParams(t)
	k, ctx, _, _ := testkeeper.GetProviderKeeperAndCtx(t, params)

	consAddr1 := sdk.ConsAddress([]byte("validator1"))
	consAddr2 := sdk.ConsAddress([]byte("validator2"))
	const consumer0 uint64 = 0
	const consumer1 uint64 = 1

	require.False(t, k.IsEpochDowntime(ctx, consumer0, consAddr1))
	require.False(t, k.IsEpochDowntime(ctx, consumer0, consAddr2))
	require.False(t, k.IsEpochDowntime(ctx, consumer1, consAddr1))

	// Mark consAddr1 on consumer0 only
	k.MarkEpochDowntime(ctx, consumer0, consAddr1)
	require.True(t, k.IsEpochDowntime(ctx, consumer0, consAddr1))
	require.False(t, k.IsEpochDowntime(ctx, consumer0, consAddr2))
	require.False(t, k.IsEpochDowntime(ctx, consumer1, consAddr1), "downtime should be per-consumer")

	// Mark consAddr2 on consumer0 too
	k.MarkEpochDowntime(ctx, consumer0, consAddr2)
	require.True(t, k.IsEpochDowntime(ctx, consumer0, consAddr1))
	require.True(t, k.IsEpochDowntime(ctx, consumer0, consAddr2))

	// Mark consAddr1 on consumer1
	k.MarkEpochDowntime(ctx, consumer1, consAddr1)
	require.True(t, k.IsEpochDowntime(ctx, consumer1, consAddr1))
	require.False(t, k.IsEpochDowntime(ctx, consumer1, consAddr2))

	// Clear all
	k.ClearEpochDowntime(ctx)
	require.False(t, k.IsEpochDowntime(ctx, consumer0, consAddr1))
	require.False(t, k.IsEpochDowntime(ctx, consumer0, consAddr2))
	require.False(t, k.IsEpochDowntime(ctx, consumer1, consAddr1))
	require.False(t, k.IsEpochDowntime(ctx, consumer1, consAddr2))
}

// TestDistributeConsumerFeesAllDowntime: when all validators have downtime,
// no outputs → early return, no bank calls.
func TestDistributeConsumerFeesAllDowntime(t *testing.T) {
	params := testkeeper.NewInMemKeeperParams(t)
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, params)
	defer ctrl.Finish()

	valAddrCodec := address.NewBech32Codec("cosmosvaloper")
	mocks.MockStakingKeeper.EXPECT().ValidatorAddressCodec().Return(valAddrCodec).AnyTimes()

	val1, _ := newBondedValidator(t, valAddrCodec, 1)
	val2, _ := newBondedValidator(t, valAddrCodec, 2)

	consAddr1, err := val1.GetConsAddr()
	require.NoError(t, err)
	consAddr2, err := val2.GetConsAddr()
	require.NoError(t, err)
	consumer0 := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, consumer0, providertypes.CONSUMER_PHASE_LAUNCHED)
	k.MarkEpochDowntime(ctx, consumer0, consAddr1)
	k.MarkEpochDowntime(ctx, consumer0, consAddr2)

	providerParams := providertypes.DefaultParams()
	providerParams.FeesPerBlockAmount = math.NewInt(10)
	k.SetParams(ctx, providerParams)

	mocks.MockStakingKeeper.EXPECT().
		GetBondedValidatorsByPower(gomock.Any()).
		Return([]stakingtypes.Validator{val1, val2}, nil)

	// No GetBalance/InputOutputCoins — all validators excluded.
	require.NoError(t, k.DistributeConsumerFees(ctx))
}

// TestDistributeConsumerFeesPropagatesBondedFetchError: error from the
// staking keeper is surfaced, not swallowed.
func TestDistributeConsumerFeesPropagatesBondedFetchError(t *testing.T) {
	params := testkeeper.NewInMemKeeperParams(t)
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, params)
	defer ctrl.Finish()

	providerParams := providertypes.DefaultParams()
	providerParams.FeesPerBlockAmount = math.NewInt(10)
	k.SetParams(ctx, providerParams)

	mocks.MockStakingKeeper.EXPECT().
		GetBondedValidatorsByPower(gomock.Any()).
		Return(nil, errors.New("boom"))

	err := k.DistributeConsumerFees(ctx)
	require.ErrorContains(t, err, "boom")
}

// TestDistributeConsumerFeesShareTooSmall: when fees_per_epoch / num_bonded
// floors to zero, nothing is sent.
func TestDistributeConsumerFeesShareTooSmall(t *testing.T) {
	params := testkeeper.NewInMemKeeperParams(t)
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, params)
	defer ctrl.Finish()

	valAddrCodec := address.NewBech32Codec("cosmosvaloper")
	mocks.MockStakingKeeper.EXPECT().ValidatorAddressCodec().Return(valAddrCodec).AnyTimes()

	val1, _ := newBondedValidator(t, valAddrCodec, 1)
	val2, _ := newBondedValidator(t, valAddrCodec, 2)

	consumer0 := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, consumer0, providertypes.CONSUMER_PHASE_LAUNCHED)

	providerParams := providertypes.DefaultParams()
	providerParams.FeesPerBlockAmount = math.NewInt(1)
	providerParams.BlocksPerEpoch = 1
	k.SetParams(ctx, providerParams)

	mocks.MockStakingKeeper.EXPECT().
		GetBondedValidatorsByPower(gomock.Any()).
		Return([]stakingtypes.Validator{val1, val2}, nil)

	// share = 1 / 2 = 0 → nothing sent
	require.NoError(t, k.DistributeConsumerFees(ctx))
}

// TestDistributeConsumerFeesZeroBalance: empty pool → in debt, no bank send.
func TestDistributeConsumerFeesZeroBalance(t *testing.T) {
	params := testkeeper.NewInMemKeeperParams(t)
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, params)
	defer ctrl.Finish()

	valAddrCodec := address.NewBech32Codec("cosmosvaloper")
	mocks.MockStakingKeeper.EXPECT().ValidatorAddressCodec().Return(valAddrCodec).AnyTimes()

	val1, _ := newBondedValidator(t, valAddrCodec, 1)
	val2, _ := newBondedValidator(t, valAddrCodec, 2)

	feesPerBlock := sdk.NewInt64Coin("uphoton", 10)

	consumer0 := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, consumer0, providertypes.CONSUMER_PHASE_LAUNCHED)

	providerParams := providertypes.DefaultParams()
	providerParams.FeesPerBlockAmount = feesPerBlock.Amount
	k.SetParams(ctx, providerParams)

	consumer0Pool := k.GetConsumerFeePoolAddress(consumer0)

	mocks.MockStakingKeeper.EXPECT().
		GetBondedValidatorsByPower(gomock.Any()).
		Return([]stakingtypes.Validator{val1, val2}, nil)

	mocks.MockBankKeeper.EXPECT().
		GetBalance(gomock.Any(), consumer0Pool, "uphoton").
		Return(sdk.NewCoin("uphoton", math.ZeroInt()))

	require.NoError(t, k.DistributeConsumerFees(ctx))
	require.True(t, k.IsConsumerInDebt(ctx, consumer0))
}

// TestEpochShareRecordsWrittenOnDistribution: a funded pool distribution
// records the computed per-validator share; an underfunded pool distribution
// (debt-skip) records a zero share. Both are recorded at the run's block time
// so a later infraction-time lookup can resolve what the share actually was.
func TestEpochShareRecordsWrittenOnDistribution(t *testing.T) {
	distributedAt := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)

	t.Run("funded pool records the computed share", func(t *testing.T) {
		params := testkeeper.NewInMemKeeperParams(t)
		k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, params)
		defer ctrl.Finish()
		ctx = ctx.WithBlockTime(distributedAt)

		valAddrCodec := address.NewBech32Codec("cosmosvaloper")
		mocks.MockStakingKeeper.EXPECT().ValidatorAddressCodec().Return(valAddrCodec).AnyTimes()

		val1, val1Bytes := newBondedValidator(t, valAddrCodec, 1)
		val2, val2Bytes := newBondedValidator(t, valAddrCodec, 2)

		feesPerBlock := sdk.NewInt64Coin("uphoton", 10)
		feesPerEpoch := sdk.NewCoin("uphoton", feesPerBlock.Amount.MulRaw(epochMultiplier))
		share := feesPerEpoch.Amount.QuoRaw(2)
		shareCoins := sdk.NewCoins(sdk.NewCoin("uphoton", share))

		consumer0 := k.FetchAndIncrementConsumerId(ctx)
		k.SetConsumerPhase(ctx, consumer0, providertypes.CONSUMER_PHASE_LAUNCHED)

		providerParams := providertypes.DefaultParams()
		providerParams.FeesPerBlockAmount = feesPerBlock.Amount
		k.SetParams(ctx, providerParams)

		consumer0Pool := k.GetConsumerFeePoolAddress(consumer0)

		mocks.MockStakingKeeper.EXPECT().
			GetBondedValidatorsByPower(gomock.Any()).
			Return([]stakingtypes.Validator{val1, val2}, nil)
		mocks.MockBankKeeper.EXPECT().
			GetBalance(gomock.Any(), consumer0Pool, "uphoton").
			Return(feesPerEpoch)
		mocks.MockBankKeeper.EXPECT().
			InputOutputCoins(gomock.Any(),
				banktypes.Input{Address: consumer0Pool.String(), Coins: sdk.NewCoins(sdk.NewCoin("uphoton", share.MulRaw(2)))},
				[]banktypes.Output{
					{Address: accAddr(val1Bytes), Coins: shareCoins},
					{Address: accAddr(val2Bytes), Coins: shareCoins},
				},
			).Return(nil)

		require.NoError(t, k.DistributeConsumerFees(ctx))

		recorded, found := k.ResolveEpochShare(ctx, consumer0, distributedAt)
		require.True(t, found)
		require.True(t, share.Equal(recorded), "want %s, got %s", share, recorded)
	})

	t.Run("underfunded pool records zero share", func(t *testing.T) {
		params := testkeeper.NewInMemKeeperParams(t)
		k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, params)
		defer ctrl.Finish()
		ctx = ctx.WithBlockTime(distributedAt)

		valAddrCodec := address.NewBech32Codec("cosmosvaloper")
		mocks.MockStakingKeeper.EXPECT().ValidatorAddressCodec().Return(valAddrCodec).AnyTimes()

		val1, _ := newBondedValidator(t, valAddrCodec, 1)
		val2, _ := newBondedValidator(t, valAddrCodec, 2)

		feesPerBlock := sdk.NewInt64Coin("uphoton", 10)
		feesPerEpoch := sdk.NewCoin("uphoton", feesPerBlock.Amount.MulRaw(epochMultiplier))

		consumer0 := k.FetchAndIncrementConsumerId(ctx)
		k.SetConsumerPhase(ctx, consumer0, providertypes.CONSUMER_PHASE_LAUNCHED)

		providerParams := providertypes.DefaultParams()
		providerParams.FeesPerBlockAmount = feesPerBlock.Amount
		k.SetParams(ctx, providerParams)

		consumer0Pool := k.GetConsumerFeePoolAddress(consumer0)

		mocks.MockStakingKeeper.EXPECT().
			GetBondedValidatorsByPower(gomock.Any()).
			Return([]stakingtypes.Validator{val1, val2}, nil)
		mocks.MockBankKeeper.EXPECT().
			GetBalance(gomock.Any(), consumer0Pool, "uphoton").
			Return(sdk.NewCoin("uphoton", feesPerEpoch.Amount.QuoRaw(2)))

		require.NoError(t, k.DistributeConsumerFees(ctx))
		require.True(t, k.IsConsumerInDebt(ctx, consumer0))

		recorded, found := k.ResolveEpochShare(ctx, consumer0, distributedAt)
		require.True(t, found)
		require.True(t, recorded.IsZero(), "want zero, got %s", recorded)
	})

	t.Run("InputOutputCoins failure records zero share", func(t *testing.T) {
		params := testkeeper.NewInMemKeeperParams(t)
		k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, params)
		defer ctrl.Finish()
		ctx = ctx.WithBlockTime(distributedAt)

		valAddrCodec := address.NewBech32Codec("cosmosvaloper")
		mocks.MockStakingKeeper.EXPECT().ValidatorAddressCodec().Return(valAddrCodec).AnyTimes()

		val1, val1Bytes := newBondedValidator(t, valAddrCodec, 1)
		val2, val2Bytes := newBondedValidator(t, valAddrCodec, 2)

		feesPerBlock := sdk.NewInt64Coin("uphoton", 10)
		feesPerEpoch := sdk.NewCoin("uphoton", feesPerBlock.Amount.MulRaw(epochMultiplier))
		share := feesPerEpoch.Amount.QuoRaw(2)
		shareCoins := sdk.NewCoins(sdk.NewCoin("uphoton", share))

		consumer0 := k.FetchAndIncrementConsumerId(ctx)
		k.SetConsumerPhase(ctx, consumer0, providertypes.CONSUMER_PHASE_LAUNCHED)

		providerParams := providertypes.DefaultParams()
		providerParams.FeesPerBlockAmount = feesPerBlock.Amount
		k.SetParams(ctx, providerParams)

		consumer0Pool := k.GetConsumerFeePoolAddress(consumer0)

		mocks.MockStakingKeeper.EXPECT().
			GetBondedValidatorsByPower(gomock.Any()).
			Return([]stakingtypes.Validator{val1, val2}, nil)
		mocks.MockBankKeeper.EXPECT().
			GetBalance(gomock.Any(), consumer0Pool, "uphoton").
			Return(feesPerEpoch)
		// InputOutputCoins fails with a generic error.
		mocks.MockBankKeeper.EXPECT().
			InputOutputCoins(gomock.Any(),
				banktypes.Input{Address: consumer0Pool.String(), Coins: sdk.NewCoins(sdk.NewCoin("uphoton", share.MulRaw(2)))},
				[]banktypes.Output{
					{Address: accAddr(val1Bytes), Coins: shareCoins},
					{Address: accAddr(val2Bytes), Coins: shareCoins},
				},
			).Return(errors.New("bank send restriction"))

		require.NoError(t, k.DistributeConsumerFees(ctx))

		recorded, found := k.ResolveEpochShare(ctx, consumer0, distributedAt)
		require.True(t, found)
		require.True(t, recorded.IsZero(), "want zero, got %s", recorded)
	})
}

// TestResolveEpochShare: given records at T1 < T2 for the same consumer,
// resolving a time in (T1, T2] returns T2's share (the run that covered it);
// resolving past T2 finds nothing (that window is still in the current,
// undistributed epoch). Pruning older than T1+1ns removes only the T1 record.
func TestResolveEpochShare(t *testing.T) {
	params := testkeeper.NewInMemKeeperParams(t)
	k, ctx, _, _ := testkeeper.GetProviderKeeperAndCtx(t, params)

	const consumerId uint64 = 0
	t1 := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 7, 8, 0, 0, 0, 0, time.UTC)
	shareT1 := math.NewInt(100)
	shareT2 := math.NewInt(200)

	k.SetEpochShareRecord(ctx, consumerId, t1, shareT1)
	k.SetEpochShareRecord(ctx, consumerId, t2, shareT2)

	// t strictly after T1 and at-or-before T2 resolves to T2's record.
	mid := t1.Add(time.Hour)
	share, found := k.ResolveEpochShare(ctx, consumerId, mid)
	require.True(t, found)
	require.True(t, shareT2.Equal(share), "want %s, got %s", shareT2, share)

	share, found = k.ResolveEpochShare(ctx, consumerId, t2)
	require.True(t, found)
	require.True(t, shareT2.Equal(share), "want %s, got %s", shareT2, share)

	// t after T2 falls in the current, not-yet-distributed epoch.
	_, found = k.ResolveEpochShare(ctx, consumerId, t2.Add(time.Second))
	require.False(t, found)

	// Prune everything strictly older than T1+1ns: only the T1 record goes.
	k.PruneEpochShareRecords(ctx, t1.Add(time.Nanosecond))

	hasT1, err := k.EpochShareRecords.Has(ctx, collections.Join(consumerId, t1.UnixNano()))
	require.NoError(t, err)
	require.False(t, hasT1, "T1 record should have been pruned")

	hasT2, err := k.EpochShareRecords.Has(ctx, collections.Join(consumerId, t2.UnixNano()))
	require.NoError(t, err)
	require.True(t, hasT2, "T2 record should survive pruning")
}
