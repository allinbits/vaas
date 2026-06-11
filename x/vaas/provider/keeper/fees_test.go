package keeper_test

import (
	"bytes"
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
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

// epochMultiplier is the default BlocksPerEpoch used in provider params.
const epochMultiplier = providertypes.DefaultBlocksPerEpoch // 600

func TestCollectFeesFromConsumers(t *testing.T) {
	params := testkeeper.NewInMemKeeperParams(t)
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, params)
	defer ctrl.Finish()

	consumer0 := k.FetchAndIncrementConsumerId(ctx)
	consumer1 := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, consumer0, providertypes.CONSUMER_PHASE_LAUNCHED)
	k.SetConsumerPhase(ctx, consumer1, providertypes.CONSUMER_PHASE_LAUNCHED)

	feesPerBlock := sdk.NewInt64Coin("uphoton", 10)
	feesPerEpoch := sdk.NewCoin("uphoton", feesPerBlock.Amount.MulRaw(epochMultiplier))
	providerParams := providertypes.DefaultParams()
	providerParams.FeesPerBlockAmount = feesPerBlock.Amount
	k.SetParams(ctx, providerParams)
	consumer0FeePoolAddr := k.GetConsumerFeePoolAddress(consumer0)
	consumer1FeePoolAddr := k.GetConsumerFeePoolAddress(consumer1)

	gomock.InOrder(
		mocks.MockBankKeeper.EXPECT().
			SendCoinsFromAccountToModule(gomock.Any(), consumer0FeePoolAddr, providertypes.ModuleName, sdk.NewCoins(feesPerEpoch)).
			Return(nil),
		mocks.MockBankKeeper.EXPECT().
			SendCoinsFromAccountToModule(gomock.Any(), consumer1FeePoolAddr, providertypes.ModuleName, sdk.NewCoins(feesPerEpoch)).
			Return(nil),
	)

	total := k.CollectFeesFromConsumers(ctx)
	expectedTotal := sdk.NewCoin("uphoton", feesPerBlock.Amount.MulRaw(epochMultiplier).MulRaw(2))
	require.Equal(t, expectedTotal, total)
	require.False(t, k.IsConsumerInDebt(ctx, consumer0))
	require.False(t, k.IsConsumerInDebt(ctx, consumer1))
}

// TestCollectFeesFromConsumers_PerConsumerOverride verifies that each consumer
// is charged its effective per-epoch fee: consumer1 has an override and pays
// the override amount * blocks_per_epoch; consumer0 has no override and pays
// the default * blocks_per_epoch.
func TestCollectFeesFromConsumers_PerConsumerOverride(t *testing.T) {
	params := testkeeper.NewInMemKeeperParams(t)
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, params)
	defer ctrl.Finish()

	consumer0 := k.FetchAndIncrementConsumerId(ctx)
	consumer1 := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, consumer0, providertypes.CONSUMER_PHASE_LAUNCHED)
	k.SetConsumerPhase(ctx, consumer1, providertypes.CONSUMER_PHASE_LAUNCHED)

	defaultFeesPerBlock := sdk.NewInt64Coin("uphoton", 10)
	overrideAmountPerBlock := math.NewInt(25)
	overrideFeesPerEpoch := sdk.NewCoin("uphoton", overrideAmountPerBlock.MulRaw(epochMultiplier))
	defaultFeesPerEpoch := sdk.NewCoin("uphoton", defaultFeesPerBlock.Amount.MulRaw(epochMultiplier))

	providerParams := providertypes.DefaultParams()
	providerParams.FeesPerBlockAmount = defaultFeesPerBlock.Amount
	k.SetParams(ctx, providerParams)

	// consumer1 gets an override; consumer0 keeps the default.
	require.NoError(t, k.ConsumerFeesPerBlockOverride.Set(ctx, consumer1, overrideAmountPerBlock))

	consumer0FeePoolAddr := k.GetConsumerFeePoolAddress(consumer0)
	consumer1FeePoolAddr := k.GetConsumerFeePoolAddress(consumer1)

	gomock.InOrder(
		mocks.MockBankKeeper.EXPECT().
			SendCoinsFromAccountToModule(gomock.Any(), consumer0FeePoolAddr, providertypes.ModuleName, sdk.NewCoins(defaultFeesPerEpoch)).
			Return(nil),
		mocks.MockBankKeeper.EXPECT().
			SendCoinsFromAccountToModule(gomock.Any(), consumer1FeePoolAddr, providertypes.ModuleName, sdk.NewCoins(overrideFeesPerEpoch)).
			Return(nil),
	)

	total := k.CollectFeesFromConsumers(ctx)
	expectedTotal := sdk.NewCoin("uphoton", defaultFeesPerEpoch.Amount.Add(overrideFeesPerEpoch.Amount))
	require.Equal(t, expectedTotal, total)
	require.False(t, k.IsConsumerInDebt(ctx, consumer0))
	require.False(t, k.IsConsumerInDebt(ctx, consumer1))
}

func TestCollectFeesFromConsumersSkipsWhenInsufficient(t *testing.T) {
	params := testkeeper.NewInMemKeeperParams(t)
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, params)
	defer ctrl.Finish()

	consumer0 := k.FetchAndIncrementConsumerId(ctx)
	consumer1 := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, consumer0, providertypes.CONSUMER_PHASE_LAUNCHED)
	k.SetConsumerPhase(ctx, consumer1, providertypes.CONSUMER_PHASE_LAUNCHED)

	feesPerBlock := sdk.NewInt64Coin("uphoton", 10)
	feesPerEpoch := sdk.NewCoin("uphoton", feesPerBlock.Amount.MulRaw(epochMultiplier))
	providerParams := providertypes.DefaultParams()
	providerParams.FeesPerBlockAmount = feesPerBlock.Amount
	k.SetParams(ctx, providerParams)
	consumer0FeePoolAddr := k.GetConsumerFeePoolAddress(consumer0)
	consumer1FeePoolAddr := k.GetConsumerFeePoolAddress(consumer1)

	gomock.InOrder(
		mocks.MockBankKeeper.EXPECT().
			SendCoinsFromAccountToModule(gomock.Any(), consumer0FeePoolAddr, providertypes.ModuleName, sdk.NewCoins(feesPerEpoch)).
			Return(sdkerrors.ErrInsufficientFunds.Wrapf("spendable 5 < 6000")),
		mocks.MockBankKeeper.EXPECT().
			SendCoinsFromAccountToModule(gomock.Any(), consumer1FeePoolAddr, providertypes.ModuleName, sdk.NewCoins(feesPerEpoch)).
			Return(nil),
	)

	total := k.CollectFeesFromConsumers(ctx)
	require.Equal(t, feesPerEpoch, total)
	require.True(t, k.IsConsumerInDebt(ctx, consumer0))
	require.False(t, k.IsConsumerInDebt(ctx, consumer1))
}

func TestCollectFeesFromConsumersClearsDebtWhenRecovered(t *testing.T) {
	params := testkeeper.NewInMemKeeperParams(t)
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, params)
	defer ctrl.Finish()

	consumer0 := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, consumer0, providertypes.CONSUMER_PHASE_LAUNCHED)
	k.SetConsumerInDebt(ctx, consumer0, true)

	feesPerBlock := sdk.NewInt64Coin("uphoton", 10)
	feesPerEpoch := sdk.NewCoin("uphoton", feesPerBlock.Amount.MulRaw(epochMultiplier))
	providerParams := providertypes.DefaultParams()
	providerParams.FeesPerBlockAmount = feesPerBlock.Amount
	k.SetParams(ctx, providerParams)
	consumer0FeePoolAddr := k.GetConsumerFeePoolAddress(consumer0)

	mocks.MockBankKeeper.EXPECT().
		SendCoinsFromAccountToModule(gomock.Any(), consumer0FeePoolAddr, providertypes.ModuleName, sdk.NewCoins(feesPerEpoch)).
		Return(nil)

	total := k.CollectFeesFromConsumers(ctx)
	require.Equal(t, feesPerEpoch, total)
	require.False(t, k.IsConsumerInDebt(ctx, consumer0))
}

// TestCollectFeesFromConsumersContinuesOnGenericError: a non-insufficient-
// funds error from the bank keeper (chain-config issue) is logged and
// skipped without flipping the debt flag — consumer0 remains not-in-debt
// because its pool is funded and the error is not its fault.
func TestCollectFeesFromConsumersContinuesOnGenericError(t *testing.T) {
	params := testkeeper.NewInMemKeeperParams(t)
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, params)
	defer ctrl.Finish()

	consumer0 := k.FetchAndIncrementConsumerId(ctx)
	consumer1 := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, consumer0, providertypes.CONSUMER_PHASE_LAUNCHED)
	k.SetConsumerPhase(ctx, consumer1, providertypes.CONSUMER_PHASE_LAUNCHED)

	feesPerBlock := sdk.NewInt64Coin("uphoton", 10)
	feesPerEpoch := sdk.NewCoin("uphoton", feesPerBlock.Amount.MulRaw(epochMultiplier))
	providerParams := providertypes.DefaultParams()
	providerParams.FeesPerBlockAmount = feesPerBlock.Amount
	k.SetParams(ctx, providerParams)
	consumer0FeePoolAddr := k.GetConsumerFeePoolAddress(consumer0)
	consumer1FeePoolAddr := k.GetConsumerFeePoolAddress(consumer1)

	gomock.InOrder(
		mocks.MockBankKeeper.EXPECT().
			SendCoinsFromAccountToModule(gomock.Any(), consumer0FeePoolAddr, providertypes.ModuleName, sdk.NewCoins(feesPerEpoch)).
			Return(errors.New("bank send restriction")),
		mocks.MockBankKeeper.EXPECT().
			SendCoinsFromAccountToModule(gomock.Any(), consumer1FeePoolAddr, providertypes.ModuleName, sdk.NewCoins(feesPerEpoch)).
			Return(nil),
	)

	total := k.CollectFeesFromConsumers(ctx)
	require.Equal(t, feesPerEpoch, total)
	require.False(t, k.IsConsumerInDebt(ctx, consumer0))
	require.False(t, k.IsConsumerInDebt(ctx, consumer1))
}

// Helpers for the distribution tests.

func newBondedValidator(t *testing.T, codec addresscodec.Codec, opSeed byte) (stakingtypes.Validator, []byte) {
	t.Helper()
	opBytes := bytes.Repeat([]byte{opSeed}, 20)
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

// TestDistributeFeesToValidators splits the pool evenly among all bonded
// validators, independent of their stake (tokens differ but shares are equal).
func TestDistributeFeesToValidators(t *testing.T) {
	params := testkeeper.NewInMemKeeperParams(t)
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, params)
	defer ctrl.Finish()

	valAddrCodec := address.NewBech32Codec("cosmosvaloper")
	mocks.MockStakingKeeper.EXPECT().ValidatorAddressCodec().Return(valAddrCodec).AnyTimes()

	val1, op1Bytes := newBondedValidator(t, valAddrCodec, 1)
	val1.Tokens = sdk.DefaultPowerReduction.MulRaw(10) // stake differs on purpose
	val2, op2Bytes := newBondedValidator(t, valAddrCodec, 2)
	val2.Tokens = sdk.DefaultPowerReduction.MulRaw(20)

	providerParams := providertypes.DefaultParams()
	providerParams.FeesPerBlockAmount = math.NewInt(10)
	k.SetParams(ctx, providerParams)

	mocks.MockBankKeeper.EXPECT().
		GetBalance(gomock.Any(), authtypes.NewModuleAddress(providertypes.ModuleName), providertypes.DefaultFeesPerBlockDenom).
		Return(sdk.NewInt64Coin("uphoton", 300))
	mocks.MockStakingKeeper.EXPECT().
		GetBondedValidatorsByPower(gomock.Any()).
		Return([]stakingtypes.Validator{val1, val2}, nil)
	// Equal split: 300 / 2 = 150 each, regardless of stake.
	mocks.MockBankKeeper.EXPECT().
		SendCoinsFromModuleToAccount(gomock.Any(), providertypes.ModuleName, sdk.AccAddress(op1Bytes), sdk.NewCoins(sdk.NewInt64Coin("uphoton", 150))).
		Return(nil)
	mocks.MockBankKeeper.EXPECT().
		SendCoinsFromModuleToAccount(gomock.Any(), providertypes.ModuleName, sdk.AccAddress(op2Bytes), sdk.NewCoins(sdk.NewInt64Coin("uphoton", 150))).
		Return(nil)

	require.NoError(t, k.DistributeFeesToValidators(ctx))
}

func TestDistributeFeesToValidatorsZeroFees(t *testing.T) {
	params := testkeeper.NewInMemKeeperParams(t)
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, params)
	defer ctrl.Finish()

	providerParams := providertypes.DefaultParams()
	providerParams.FeesPerBlockAmount = math.NewInt(10)
	k.SetParams(ctx, providerParams)

	mocks.MockBankKeeper.EXPECT().
		GetBalance(gomock.Any(), authtypes.NewModuleAddress(providertypes.ModuleName), providertypes.DefaultFeesPerBlockDenom).
		Return(sdk.NewCoin("uphoton", math.ZeroInt()))

	require.NoError(t, k.DistributeFeesToValidators(ctx))
}

// TestDistributeFeesToValidatorsSkipsWhenFeesTooSmall: pool holds 1 coin,
// 2 validators → floor(1/2) = 0 → nothing sent, balance stays pooled.
func TestDistributeFeesToValidatorsSkipsWhenFeesTooSmall(t *testing.T) {
	params := testkeeper.NewInMemKeeperParams(t)
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, params)
	defer ctrl.Finish()

	valAddrCodec := address.NewBech32Codec("cosmosvaloper")
	mocks.MockStakingKeeper.EXPECT().ValidatorAddressCodec().Return(valAddrCodec).AnyTimes()

	val1, _ := newBondedValidator(t, valAddrCodec, 1)
	val2, _ := newBondedValidator(t, valAddrCodec, 2)

	providerParams := providertypes.DefaultParams()
	providerParams.FeesPerBlockAmount = math.NewInt(1)
	k.SetParams(ctx, providerParams)

	mocks.MockBankKeeper.EXPECT().
		GetBalance(gomock.Any(), authtypes.NewModuleAddress(providertypes.ModuleName), providertypes.DefaultFeesPerBlockDenom).
		Return(sdk.NewInt64Coin("uphoton", 1))
	mocks.MockStakingKeeper.EXPECT().
		GetBondedValidatorsByPower(gomock.Any()).
		Return([]stakingtypes.Validator{val1, val2}, nil)

	require.NoError(t, k.DistributeFeesToValidators(ctx))
}

// TestDistributeFeesToValidatorsRemainderStaysPooled: 10 coins / 3 validators
// → each gets floor(10/3) = 3, 1 coin stays in the pool.
func TestDistributeFeesToValidatorsRemainderStaysPooled(t *testing.T) {
	params := testkeeper.NewInMemKeeperParams(t)
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, params)
	defer ctrl.Finish()

	valAddrCodec := address.NewBech32Codec("cosmosvaloper")
	mocks.MockStakingKeeper.EXPECT().ValidatorAddressCodec().Return(valAddrCodec).AnyTimes()

	val1, op1 := newBondedValidator(t, valAddrCodec, 1)
	val2, op2 := newBondedValidator(t, valAddrCodec, 2)
	val3, op3 := newBondedValidator(t, valAddrCodec, 3)

	providerParams := providertypes.DefaultParams()
	providerParams.FeesPerBlockAmount = math.NewInt(10)
	k.SetParams(ctx, providerParams)

	mocks.MockBankKeeper.EXPECT().
		GetBalance(gomock.Any(), authtypes.NewModuleAddress(providertypes.ModuleName), providertypes.DefaultFeesPerBlockDenom).
		Return(sdk.NewInt64Coin("uphoton", 10))
	mocks.MockStakingKeeper.EXPECT().
		GetBondedValidatorsByPower(gomock.Any()).
		Return([]stakingtypes.Validator{val1, val2, val3}, nil)
	share := sdk.NewCoins(sdk.NewInt64Coin("uphoton", 3))
	for _, op := range [][]byte{op1, op2, op3} {
		mocks.MockBankKeeper.EXPECT().
			SendCoinsFromModuleToAccount(gomock.Any(), providertypes.ModuleName, sdk.AccAddress(op), share).
			Return(nil)
	}

	require.NoError(t, k.DistributeFeesToValidators(ctx))
}

// TestDistributeFeesToValidatorsNoBondedValidators: no bonded validators → nothing sent.
func TestDistributeFeesToValidatorsNoBondedValidators(t *testing.T) {
	params := testkeeper.NewInMemKeeperParams(t)
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, params)
	defer ctrl.Finish()

	providerParams := providertypes.DefaultParams()
	providerParams.FeesPerBlockAmount = math.NewInt(50)
	k.SetParams(ctx, providerParams)

	mocks.MockBankKeeper.EXPECT().
		GetBalance(gomock.Any(), authtypes.NewModuleAddress(providertypes.ModuleName), providertypes.DefaultFeesPerBlockDenom).
		Return(sdk.NewInt64Coin("uphoton", 50))
	mocks.MockStakingKeeper.EXPECT().
		GetBondedValidatorsByPower(gomock.Any()).
		Return(nil, nil)

	require.NoError(t, k.DistributeFeesToValidators(ctx))
}

// TestDistributeFeesToValidatorsIncludesOfflineValidators: all bonded
// validators receive an equal share regardless of signing status.
func TestDistributeFeesToValidatorsIncludesOfflineValidators(t *testing.T) {
	params := testkeeper.NewInMemKeeperParams(t)
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, params)
	defer ctrl.Finish()

	valAddrCodec := address.NewBech32Codec("cosmosvaloper")
	mocks.MockStakingKeeper.EXPECT().ValidatorAddressCodec().Return(valAddrCodec).AnyTimes()

	val1, op1 := newBondedValidator(t, valAddrCodec, 1)
	val2, op2 := newBondedValidator(t, valAddrCodec, 2)

	providerParams := providertypes.DefaultParams()
	providerParams.FeesPerBlockAmount = math.NewInt(10)
	k.SetParams(ctx, providerParams)

	mocks.MockBankKeeper.EXPECT().
		GetBalance(gomock.Any(), authtypes.NewModuleAddress(providertypes.ModuleName), providertypes.DefaultFeesPerBlockDenom).
		Return(sdk.NewInt64Coin("uphoton", 100))
	mocks.MockStakingKeeper.EXPECT().
		GetBondedValidatorsByPower(gomock.Any()).
		Return([]stakingtypes.Validator{val1, val2}, nil)
	// Both validators get an equal share (100/2 = 50), even if one was offline.
	share := sdk.NewCoins(sdk.NewInt64Coin("uphoton", 50))
	mocks.MockBankKeeper.EXPECT().
		SendCoinsFromModuleToAccount(gomock.Any(), providertypes.ModuleName, sdk.AccAddress(op1), share).
		Return(nil)
	mocks.MockBankKeeper.EXPECT().
		SendCoinsFromModuleToAccount(gomock.Any(), providertypes.ModuleName, sdk.AccAddress(op2), share).
		Return(nil)

	require.NoError(t, k.DistributeFeesToValidators(ctx))
}

// TestDistributeFeesToValidatorsPropagatesBondedFetchError: error from the
// staking keeper is surfaced, not swallowed.
func TestDistributeFeesToValidatorsPropagatesBondedFetchError(t *testing.T) {
	params := testkeeper.NewInMemKeeperParams(t)
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, params)
	defer ctrl.Finish()

	providerParams := providertypes.DefaultParams()
	providerParams.FeesPerBlockAmount = math.NewInt(10)
	k.SetParams(ctx, providerParams)

	mocks.MockBankKeeper.EXPECT().
		GetBalance(gomock.Any(), authtypes.NewModuleAddress(providertypes.ModuleName), providertypes.DefaultFeesPerBlockDenom).
		Return(sdk.NewInt64Coin("uphoton", 100))
	mocks.MockStakingKeeper.EXPECT().
		GetBondedValidatorsByPower(gomock.Any()).
		Return(nil, errors.New("boom"))

	err := k.DistributeFeesToValidators(ctx)
	require.ErrorContains(t, err, "boom")
}

// TestDistributeFeesToValidatorsExcludesDowntime: validators with epoch
// downtime evidence do not receive rewards, and their share is NOT
// redistributed to others (the share stays in the module account).
func TestDistributeFeesToValidatorsExcludesDowntime(t *testing.T) {
	params := testkeeper.NewInMemKeeperParams(t)
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, params)
	defer ctrl.Finish()

	valAddrCodec := address.NewBech32Codec("cosmosvaloper")
	mocks.MockStakingKeeper.EXPECT().ValidatorAddressCodec().Return(valAddrCodec).AnyTimes()

	val1, op1 := newBondedValidator(t, valAddrCodec, 1)
	val2, _ := newBondedValidator(t, valAddrCodec, 2)

	consAddr1, err := val1.GetConsAddr()
	require.NoError(t, err)

	providerParams := providertypes.DefaultParams()
	providerParams.FeesPerBlockAmount = math.NewInt(10)
	k.SetParams(ctx, providerParams)

	// Mark val2 as having downtime
	consAddr2, err := val2.GetConsAddr()
	require.NoError(t, err)
	k.MarkEpochDowntime(ctx, consAddr2)

	mocks.MockBankKeeper.EXPECT().
		GetBalance(gomock.Any(), authtypes.NewModuleAddress(providertypes.ModuleName), providertypes.DefaultFeesPerBlockDenom).
		Return(sdk.NewInt64Coin("uphoton", 100))
	mocks.MockStakingKeeper.EXPECT().
		GetBondedValidatorsByPower(gomock.Any()).
		Return([]stakingtypes.Validator{val1, val2}, nil)

	// Share = 100 / 2 = 50 per validator. val2 is excluded but the share
	// amount stays the same for val1 (no redistribution).
	share := sdk.NewCoins(sdk.NewInt64Coin("uphoton", 50))
	mocks.MockBankKeeper.EXPECT().
		SendCoinsFromModuleToAccount(gomock.Any(), providertypes.ModuleName, sdk.AccAddress(op1), share).
		Return(nil)
	// val2 is NOT paid — no mock for val2's send.

	// val1 should not be in downtime set
	require.False(t, k.IsEpochDowntime(ctx, consAddr1))
	require.True(t, k.IsEpochDowntime(ctx, consAddr2))

	require.NoError(t, k.DistributeFeesToValidators(ctx))
}

// TestEpochDowntimeTracking tests the lifecycle: mark, check, clear.
func TestEpochDowntimeTracking(t *testing.T) {
	params := testkeeper.NewInMemKeeperParams(t)
	k, ctx, _, _ := testkeeper.GetProviderKeeperAndCtx(t, params)

	consAddr1 := sdk.ConsAddress([]byte("validator1"))
	consAddr2 := sdk.ConsAddress([]byte("validator2"))

	// Initially empty
	require.False(t, k.IsEpochDowntime(ctx, consAddr1))
	require.False(t, k.IsEpochDowntime(ctx, consAddr2))

	// Mark one
	k.MarkEpochDowntime(ctx, consAddr1)
	require.True(t, k.IsEpochDowntime(ctx, consAddr1))
	require.False(t, k.IsEpochDowntime(ctx, consAddr2))

	// Mark another
	k.MarkEpochDowntime(ctx, consAddr2)
	require.True(t, k.IsEpochDowntime(ctx, consAddr1))
	require.True(t, k.IsEpochDowntime(ctx, consAddr2))

	// Clear all
	k.ClearEpochDowntime(ctx)
	require.False(t, k.IsEpochDowntime(ctx, consAddr1))
	require.False(t, k.IsEpochDowntime(ctx, consAddr2))
}

// TestDistributeFeesToValidatorsAllDowntime: when all validators have
// downtime, nobody gets paid. The full balance stays in the module account.
func TestDistributeFeesToValidatorsAllDowntime(t *testing.T) {
	params := testkeeper.NewInMemKeeperParams(t)
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, params)
	defer ctrl.Finish()

	valAddrCodec := address.NewBech32Codec("cosmosvaloper")
	mocks.MockStakingKeeper.EXPECT().ValidatorAddressCodec().Return(valAddrCodec).AnyTimes()

	val1, _ := newBondedValidator(t, valAddrCodec, 1)
	val2, _ := newBondedValidator(t, valAddrCodec, 2)

	// Mark both as downtime
	consAddr1, err := val1.GetConsAddr()
	require.NoError(t, err)
	consAddr2, err := val2.GetConsAddr()
	require.NoError(t, err)
	k.MarkEpochDowntime(ctx, consAddr1)
	k.MarkEpochDowntime(ctx, consAddr2)

	providerParams := providertypes.DefaultParams()
	providerParams.FeesPerBlockAmount = math.NewInt(10)
	k.SetParams(ctx, providerParams)

	mocks.MockBankKeeper.EXPECT().
		GetBalance(gomock.Any(), authtypes.NewModuleAddress(providertypes.ModuleName), providertypes.DefaultFeesPerBlockDenom).
		Return(sdk.NewInt64Coin("uphoton", 100))
	mocks.MockStakingKeeper.EXPECT().
		GetBondedValidatorsByPower(gomock.Any()).
		Return([]stakingtypes.Validator{val1, val2}, nil)

	// No bank sends expected — both validators are excluded.
	require.NoError(t, k.DistributeFeesToValidators(ctx))
}
