package keeper_test

import (
	"errors"
	"testing"

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

// TestDistributeConsumerFees splits each consumer's fees directly to bonded
// validators via a single InputOutputCoins call.
func TestDistributeConsumerFees(t *testing.T) {
	params := testkeeper.NewInMemKeeperParams(t)
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, params)
	defer ctrl.Finish()

	valAddrCodec := address.NewBech32Codec("cosmosvaloper")
	mocks.MockStakingKeeper.EXPECT().ValidatorAddressCodec().Return(valAddrCodec).AnyTimes()

	val1, _ := newBondedValidator(t, valAddrCodec, 1)
	val1.Tokens = sdk.DefaultPowerReduction.MulRaw(10)
	val2, _ := newBondedValidator(t, valAddrCodec, 2)
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
				{Address: val1.GetOperator(), Coins: shareCoins},
				{Address: val2.GetOperator(), Coins: shareCoins},
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
				{Address: val1.GetOperator(), Coins: shareCoins},
				{Address: val2.GetOperator(), Coins: shareCoins},
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

	val1, _ := newBondedValidator(t, valAddrCodec, 1)
	val2, _ := newBondedValidator(t, valAddrCodec, 2)

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
				{Address: val1.GetOperator(), Coins: shareCoins},
				{Address: val2.GetOperator(), Coins: shareCoins},
			},
		).Return(nil)

	require.NoError(t, k.DistributeConsumerFees(ctx))
	require.False(t, k.IsConsumerInDebt(ctx, consumer0))
}

// TestDistributeConsumerFeesContinuesOnGenericError: InputOutputCoins fails
// with a non-insufficient-funds error — logged, debt flag unchanged.
func TestDistributeConsumerFeesContinuesOnGenericError(t *testing.T) {
	params := testkeeper.NewInMemKeeperParams(t)
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, params)
	defer ctrl.Finish()

	valAddrCodec := address.NewBech32Codec("cosmosvaloper")
	mocks.MockStakingKeeper.EXPECT().ValidatorAddressCodec().Return(valAddrCodec).AnyTimes()

	val1, _ := newBondedValidator(t, valAddrCodec, 1)
	val2, _ := newBondedValidator(t, valAddrCodec, 2)

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
				{Address: val1.GetOperator(), Coins: shareCoins},
				{Address: val2.GetOperator(), Coins: shareCoins},
			},
		).Return(errors.New("bank send restriction"))

	require.NoError(t, k.DistributeConsumerFees(ctx))
	require.False(t, k.IsConsumerInDebt(ctx, consumer0))
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

	val1, _ := newBondedValidator(t, valAddrCodec, 1)
	val2, _ := newBondedValidator(t, valAddrCodec, 2)

	consAddr1, err := val1.GetConsAddr()
	require.NoError(t, err)
	consAddr2, err := val2.GetConsAddr()
	require.NoError(t, err)
	k.MarkEpochDowntime(ctx, consAddr2)

	feesPerBlock := sdk.NewInt64Coin("uphoton", 10)
	feesPerEpoch := sdk.NewCoin("uphoton", feesPerBlock.Amount.MulRaw(epochMultiplier))
	share := feesPerEpoch.Amount.QuoRaw(2) // share = total / num_bonded (not eligible)
	shareCoins := sdk.NewCoins(sdk.NewCoin("uphoton", share))

	consumer0 := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, consumer0, providertypes.CONSUMER_PHASE_LAUNCHED)

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
				{Address: val1.GetOperator(), Coins: shareCoins},
			},
		).Return(nil)

	require.False(t, k.IsEpochDowntime(ctx, consAddr1))
	require.True(t, k.IsEpochDowntime(ctx, consAddr2))
	require.NoError(t, k.DistributeConsumerFees(ctx))
	require.False(t, k.IsConsumerInDebt(ctx, consumer0))
}

// TestEpochDowntimeTracking tests the lifecycle: mark, check, clear.
func TestEpochDowntimeTracking(t *testing.T) {
	params := testkeeper.NewInMemKeeperParams(t)
	k, ctx, _, _ := testkeeper.GetProviderKeeperAndCtx(t, params)

	consAddr1 := sdk.ConsAddress([]byte("validator1"))
	consAddr2 := sdk.ConsAddress([]byte("validator2"))

	require.False(t, k.IsEpochDowntime(ctx, consAddr1))
	require.False(t, k.IsEpochDowntime(ctx, consAddr2))

	k.MarkEpochDowntime(ctx, consAddr1)
	require.True(t, k.IsEpochDowntime(ctx, consAddr1))
	require.False(t, k.IsEpochDowntime(ctx, consAddr2))

	k.MarkEpochDowntime(ctx, consAddr2)
	require.True(t, k.IsEpochDowntime(ctx, consAddr1))
	require.True(t, k.IsEpochDowntime(ctx, consAddr2))

	k.ClearEpochDowntime(ctx)
	require.False(t, k.IsEpochDowntime(ctx, consAddr1))
	require.False(t, k.IsEpochDowntime(ctx, consAddr2))
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
	k.MarkEpochDowntime(ctx, consAddr1)
	k.MarkEpochDowntime(ctx, consAddr2)

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
