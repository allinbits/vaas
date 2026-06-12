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
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

// epochMultiplier is the default BlocksPerEpoch used in provider params.
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
// validators, independent of stake.
func TestDistributeConsumerFees(t *testing.T) {
	params := testkeeper.NewInMemKeeperParams(t)
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, params)
	defer ctrl.Finish()

	valAddrCodec := address.NewBech32Codec("cosmosvaloper")
	mocks.MockStakingKeeper.EXPECT().ValidatorAddressCodec().Return(valAddrCodec).AnyTimes()

	val1, op1Bytes := newBondedValidator(t, valAddrCodec, 1)
	val1.Tokens = sdk.DefaultPowerReduction.MulRaw(10)
	val2, op2Bytes := newBondedValidator(t, valAddrCodec, 2)
	val2.Tokens = sdk.DefaultPowerReduction.MulRaw(20)

	feesPerBlock := sdk.NewInt64Coin("uphoton", 10)
	feesPerEpoch := sdk.NewCoin("uphoton", feesPerBlock.Amount.MulRaw(epochMultiplier))
	sharePerVal := feesPerEpoch.Amount.QuoRaw(2) // 3000
	shareCoins := sdk.NewCoins(sdk.NewCoin("uphoton", sharePerVal))

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

	// consumer0: sends sharePerVal to val1 and val2
	mocks.MockBankKeeper.EXPECT().
		SendCoins(gomock.Any(), consumer0Pool, sdk.AccAddress(op1Bytes), shareCoins).
		Return(nil)
	mocks.MockBankKeeper.EXPECT().
		SendCoins(gomock.Any(), consumer0Pool, sdk.AccAddress(op2Bytes), shareCoins).
		Return(nil)
	// consumer1: sends sharePerVal to val1 and val2
	mocks.MockBankKeeper.EXPECT().
		SendCoins(gomock.Any(), consumer1Pool, sdk.AccAddress(op1Bytes), shareCoins).
		Return(nil)
	mocks.MockBankKeeper.EXPECT().
		SendCoins(gomock.Any(), consumer1Pool, sdk.AccAddress(op2Bytes), shareCoins).
		Return(nil)

	require.NoError(t, k.DistributeConsumerFees(ctx))
	require.False(t, k.IsConsumerInDebt(ctx, consumer0))
	require.False(t, k.IsConsumerInDebt(ctx, consumer1))
}

// TestDistributeConsumerFeesPerConsumerOverride verifies that each consumer
// pays its effective per-epoch fee.
func TestDistributeConsumerFeesPerConsumerOverride(t *testing.T) {
	params := testkeeper.NewInMemKeeperParams(t)
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, params)
	defer ctrl.Finish()

	valAddrCodec := address.NewBech32Codec("cosmosvaloper")
	mocks.MockStakingKeeper.EXPECT().ValidatorAddressCodec().Return(valAddrCodec).AnyTimes()

	val1, op1Bytes := newBondedValidator(t, valAddrCodec, 1)
	val2, op2Bytes := newBondedValidator(t, valAddrCodec, 2)

	defaultFeesPerBlock := sdk.NewInt64Coin("uphoton", 10)
	overrideAmountPerBlock := math.NewInt(25)
	defaultFeesPerEpoch := sdk.NewCoin("uphoton", defaultFeesPerBlock.Amount.MulRaw(epochMultiplier))
	overrideFeesPerEpoch := sdk.NewCoin("uphoton", overrideAmountPerBlock.MulRaw(epochMultiplier))

	providerParams := providertypes.DefaultParams()
	providerParams.FeesPerBlockAmount = defaultFeesPerBlock.Amount
	k.SetParams(ctx, providerParams)

	// consumer1 gets an override
	require.NoError(t, k.ConsumerFeesPerBlockOverride.Set(ctx, 1, overrideAmountPerBlock))

	consumer0 := k.FetchAndIncrementConsumerId(ctx)
	consumer1 := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, consumer0, providertypes.CONSUMER_PHASE_LAUNCHED)
	k.SetConsumerPhase(ctx, consumer1, providertypes.CONSUMER_PHASE_LAUNCHED)

	consumer0Pool := k.GetConsumerFeePoolAddress(consumer0)
	consumer1Pool := k.GetConsumerFeePoolAddress(consumer1)

	mocks.MockStakingKeeper.EXPECT().
		GetBondedValidatorsByPower(gomock.Any()).
		Return([]stakingtypes.Validator{val1, val2}, nil)

	// consumer0: default share = 6000 / 2 = 3000 per validator
	defaultShare := sdk.NewCoins(sdk.NewCoin("uphoton", defaultFeesPerEpoch.Amount.QuoRaw(2)))
	mocks.MockBankKeeper.EXPECT().
		SendCoins(gomock.Any(), consumer0Pool, sdk.AccAddress(op1Bytes), defaultShare).
		Return(nil)
	mocks.MockBankKeeper.EXPECT().
		SendCoins(gomock.Any(), consumer0Pool, sdk.AccAddress(op2Bytes), defaultShare).
		Return(nil)

	// consumer1: override share = 15000 / 2 = 7500 per validator
	overrideShare := sdk.NewCoins(sdk.NewCoin("uphoton", overrideFeesPerEpoch.Amount.QuoRaw(2)))
	mocks.MockBankKeeper.EXPECT().
		SendCoins(gomock.Any(), consumer1Pool, sdk.AccAddress(op1Bytes), overrideShare).
		Return(nil)
	mocks.MockBankKeeper.EXPECT().
		SendCoins(gomock.Any(), consumer1Pool, sdk.AccAddress(op2Bytes), overrideShare).
		Return(nil)

	require.NoError(t, k.DistributeConsumerFees(ctx))
	require.False(t, k.IsConsumerInDebt(ctx, consumer0))
	require.False(t, k.IsConsumerInDebt(ctx, consumer1))
}

// TestDistributeConsumerFeesSkipsInsufficient: one consumer is underfunded
// (marked as debt), the other pays normally.
func TestDistributeConsumerFeesSkipsInsufficient(t *testing.T) {
	params := testkeeper.NewInMemKeeperParams(t)
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, params)
	defer ctrl.Finish()

	valAddrCodec := address.NewBech32Codec("cosmosvaloper")
	mocks.MockStakingKeeper.EXPECT().ValidatorAddressCodec().Return(valAddrCodec).AnyTimes()

	val1, op1Bytes := newBondedValidator(t, valAddrCodec, 1)
	val2, op2Bytes := newBondedValidator(t, valAddrCodec, 2)

	feesPerBlock := sdk.NewInt64Coin("uphoton", 10)
	feesPerEpoch := sdk.NewCoin("uphoton", feesPerBlock.Amount.MulRaw(epochMultiplier))
	sharePerVal := feesPerEpoch.Amount.QuoRaw(2)
	shareCoins := sdk.NewCoins(sdk.NewCoin("uphoton", sharePerVal))

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

	// consumer0: first SendCoins fails with ErrInsufficientFunds
	mocks.MockBankKeeper.EXPECT().
		SendCoins(gomock.Any(), consumer0Pool, sdk.AccAddress(op1Bytes), shareCoins).
		Return(sdkerrors.ErrInsufficientFunds.Wrapf("spendable 5 < 3000"))

	// consumer1: succeeds
	mocks.MockBankKeeper.EXPECT().
		SendCoins(gomock.Any(), consumer1Pool, sdk.AccAddress(op1Bytes), shareCoins).
		Return(nil)
	mocks.MockBankKeeper.EXPECT().
		SendCoins(gomock.Any(), consumer1Pool, sdk.AccAddress(op2Bytes), shareCoins).
		Return(nil)

	require.NoError(t, k.DistributeConsumerFees(ctx))
	require.True(t, k.IsConsumerInDebt(ctx, consumer0))
	require.False(t, k.IsConsumerInDebt(ctx, consumer1))
}

// TestDistributeConsumerFeesClearsDebtWhenRecovered: a consumer previously in
// debt pays successfully and the flag is cleared.
func TestDistributeConsumerFeesClearsDebtWhenRecovered(t *testing.T) {
	params := testkeeper.NewInMemKeeperParams(t)
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, params)
	defer ctrl.Finish()

	valAddrCodec := address.NewBech32Codec("cosmosvaloper")
	mocks.MockStakingKeeper.EXPECT().ValidatorAddressCodec().Return(valAddrCodec).AnyTimes()

	val1, op1Bytes := newBondedValidator(t, valAddrCodec, 1)
	val2, op2Bytes := newBondedValidator(t, valAddrCodec, 2)

	consumer0 := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, consumer0, providertypes.CONSUMER_PHASE_LAUNCHED)
	k.SetConsumerInDebt(ctx, consumer0, true)

	feesPerBlock := sdk.NewInt64Coin("uphoton", 10)
	feesPerEpoch := sdk.NewCoin("uphoton", feesPerBlock.Amount.MulRaw(epochMultiplier))
	sharePerVal := feesPerEpoch.Amount.QuoRaw(2)
	shareCoins := sdk.NewCoins(sdk.NewCoin("uphoton", sharePerVal))

	providerParams := providertypes.DefaultParams()
	providerParams.FeesPerBlockAmount = feesPerBlock.Amount
	k.SetParams(ctx, providerParams)

	consumer0Pool := k.GetConsumerFeePoolAddress(consumer0)

	mocks.MockStakingKeeper.EXPECT().
		GetBondedValidatorsByPower(gomock.Any()).
		Return([]stakingtypes.Validator{val1, val2}, nil)
	mocks.MockBankKeeper.EXPECT().
		SendCoins(gomock.Any(), consumer0Pool, sdk.AccAddress(op1Bytes), shareCoins).
		Return(nil)
	mocks.MockBankKeeper.EXPECT().
		SendCoins(gomock.Any(), consumer0Pool, sdk.AccAddress(op2Bytes), shareCoins).
		Return(nil)

	require.NoError(t, k.DistributeConsumerFees(ctx))
	require.False(t, k.IsConsumerInDebt(ctx, consumer0))
}

// TestDistributeConsumerFeesContinuesOnGenericError: a non-insufficient-funds
// error from the bank keeper (chain-config issue) is logged and skipped
// without flipping the debt flag — the consumer remains not-in-debt.
func TestDistributeConsumerFeesContinuesOnGenericError(t *testing.T) {
	params := testkeeper.NewInMemKeeperParams(t)
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, params)
	defer ctrl.Finish()

	valAddrCodec := address.NewBech32Codec("cosmosvaloper")
	mocks.MockStakingKeeper.EXPECT().ValidatorAddressCodec().Return(valAddrCodec).AnyTimes()

	val1, op1Bytes := newBondedValidator(t, valAddrCodec, 1)
	val2, op2Bytes := newBondedValidator(t, valAddrCodec, 2)

	feesPerBlock := sdk.NewInt64Coin("uphoton", 10)
	feesPerEpoch := sdk.NewCoin("uphoton", feesPerBlock.Amount.MulRaw(epochMultiplier))
	sharePerVal := feesPerEpoch.Amount.QuoRaw(2)
	shareCoins := sdk.NewCoins(sdk.NewCoin("uphoton", sharePerVal))

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

	// consumer0: generic error on first send → break, not marked as debt
	mocks.MockBankKeeper.EXPECT().
		SendCoins(gomock.Any(), consumer0Pool, sdk.AccAddress(op1Bytes), shareCoins).
		Return(errors.New("bank send restriction"))

	// consumer1: succeeds
	mocks.MockBankKeeper.EXPECT().
		SendCoins(gomock.Any(), consumer1Pool, sdk.AccAddress(op1Bytes), shareCoins).
		Return(nil)
	mocks.MockBankKeeper.EXPECT().
		SendCoins(gomock.Any(), consumer1Pool, sdk.AccAddress(op2Bytes), shareCoins).
		Return(nil)

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

	// No SendCoins expected — consumer0 is REGISTERED, not LAUNCHED.
	require.NoError(t, k.DistributeConsumerFees(ctx))
}

// TestDistributeConsumerFeesExcludesDowntime: validators with epoch downtime
// evidence do not receive rewards. Their share stays in the consumer pool.
func TestDistributeConsumerFeesExcludesDowntime(t *testing.T) {
	params := testkeeper.NewInMemKeeperParams(t)
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, params)
	defer ctrl.Finish()

	valAddrCodec := address.NewBech32Codec("cosmosvaloper")
	mocks.MockStakingKeeper.EXPECT().ValidatorAddressCodec().Return(valAddrCodec).AnyTimes()

	val1, op1 := newBondedValidator(t, valAddrCodec, 1)
	val2, _ := newBondedValidator(t, valAddrCodec, 2)

	consAddr1, err := val1.GetConsAddr()
	require.NoError(t, err)

	// Mark val2 as having downtime
	consAddr2, err := val2.GetConsAddr()
	require.NoError(t, err)
	k.MarkEpochDowntime(ctx, consAddr2)

	feesPerBlock := sdk.NewInt64Coin("uphoton", 10)
	feesPerEpoch := sdk.NewCoin("uphoton", feesPerBlock.Amount.MulRaw(epochMultiplier))
	sharePerVal := feesPerEpoch.Amount.QuoRaw(2) // share = total / num_bonded (not eligible)
	shareCoins := sdk.NewCoins(sdk.NewCoin("uphoton", sharePerVal))

	consumer0 := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, consumer0, providertypes.CONSUMER_PHASE_LAUNCHED)

	providerParams := providertypes.DefaultParams()
	providerParams.FeesPerBlockAmount = math.NewInt(10)
	k.SetParams(ctx, providerParams)

	consumer0Pool := k.GetConsumerFeePoolAddress(consumer0)

	mocks.MockStakingKeeper.EXPECT().
		GetBondedValidatorsByPower(gomock.Any()).
		Return([]stakingtypes.Validator{val1, val2}, nil)

	// Only val1 receives a share. val2 is skipped (downtime).
	// val2's share stays in consumer0's pool — never sent.
	mocks.MockBankKeeper.EXPECT().
		SendCoins(gomock.Any(), consumer0Pool, sdk.AccAddress(op1), shareCoins).
		Return(nil)

	// val1 should not be in downtime set
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
// nobody gets paid. The full balance stays in the consumer fee pools.
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

	// No SendCoins expected — both validators are excluded.
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

	// 1 uphoton per block * 600 = 600 per epoch. 600 / 2 = 300 per validator.
	// Use fees_per_block = 1, blocks_per_epoch = 1 to make share = 0 (1/2 = 0).
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

// TestDistributeConsumerFeesSkipsWhenInsufficientMidWay: when the consumer
// pool runs out mid-distribution, partial validators get paid and the
// consumer is marked as in debt.
func TestDistributeConsumerFeesSkipsWhenInsufficientMidWay(t *testing.T) {
	params := testkeeper.NewInMemKeeperParams(t)
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, params)
	defer ctrl.Finish()

	valAddrCodec := address.NewBech32Codec("cosmosvaloper")
	mocks.MockStakingKeeper.EXPECT().ValidatorAddressCodec().Return(valAddrCodec).AnyTimes()

	val1, op1 := newBondedValidator(t, valAddrCodec, 1)
	val2, op2 := newBondedValidator(t, valAddrCodec, 2)

	feesPerBlock := sdk.NewInt64Coin("uphoton", 10)
	feesPerEpoch := sdk.NewCoin("uphoton", feesPerBlock.Amount.MulRaw(epochMultiplier))
	sharePerVal := feesPerEpoch.Amount.QuoRaw(2)
	shareCoins := sdk.NewCoins(sdk.NewCoin("uphoton", sharePerVal))

	consumer0 := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, consumer0, providertypes.CONSUMER_PHASE_LAUNCHED)

	providerParams := providertypes.DefaultParams()
	providerParams.FeesPerBlockAmount = feesPerBlock.Amount
	k.SetParams(ctx, providerParams)

	consumer0Pool := k.GetConsumerFeePoolAddress(consumer0)

	mocks.MockStakingKeeper.EXPECT().
		GetBondedValidatorsByPower(gomock.Any()).
		Return([]stakingtypes.Validator{val1, val2}, nil)

	// First send succeeds, second fails with ErrInsufficientFunds
	mocks.MockBankKeeper.EXPECT().
		SendCoins(gomock.Any(), consumer0Pool, sdk.AccAddress(op1), shareCoins).
		Return(nil)
	mocks.MockBankKeeper.EXPECT().
		SendCoins(gomock.Any(), consumer0Pool, sdk.AccAddress(op2), shareCoins).
		Return(sdkerrors.ErrInsufficientFunds.Wrapf("spendable 0 < 3000"))

	require.NoError(t, k.DistributeConsumerFees(ctx))
	// Consumer is in debt because we hit ErrInsufficientFunds mid-way
	require.True(t, k.IsConsumerInDebt(ctx, consumer0))
}
