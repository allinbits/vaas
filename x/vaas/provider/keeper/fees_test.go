package keeper_test

import (
	"bytes"
	"errors"
	"testing"

	abci "github.com/cometbft/cometbft/abci/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"

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

func TestCollectFeesFromConsumers(t *testing.T) {
	params := testkeeper.NewInMemKeeperParams(t)
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, params)
	defer ctrl.Finish()

	consumer0 := k.FetchAndIncrementConsumerId(ctx)
	consumer1 := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, consumer0, providertypes.CONSUMER_PHASE_LAUNCHED)
	k.SetConsumerPhase(ctx, consumer1, providertypes.CONSUMER_PHASE_LAUNCHED)

	feesPerBlock := sdk.NewInt64Coin("uphoton", 10)
	providerParams := providertypes.DefaultParams()
	providerParams.FeesPerBlock = feesPerBlock
	k.SetParams(ctx, providerParams)
	consumer0FeePoolAddr := k.GetConsumerFeePoolAddress(consumer0)
	consumer1FeePoolAddr := k.GetConsumerFeePoolAddress(consumer1)

	gomock.InOrder(
		mocks.MockBankKeeper.EXPECT().
			SendCoinsFromAccountToModule(gomock.Any(), consumer0FeePoolAddr, providertypes.ModuleName, sdk.NewCoins(feesPerBlock)).
			Return(nil),
		mocks.MockBankKeeper.EXPECT().
			SendCoinsFromAccountToModule(gomock.Any(), consumer1FeePoolAddr, providertypes.ModuleName, sdk.NewCoins(feesPerBlock)).
			Return(nil),
	)

	total := k.CollectFeesFromConsumers(ctx)
	require.Equal(t, sdk.NewInt64Coin("uphoton", 20), total)
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
	providerParams := providertypes.DefaultParams()
	providerParams.FeesPerBlock = feesPerBlock
	k.SetParams(ctx, providerParams)
	consumer0FeePoolAddr := k.GetConsumerFeePoolAddress(consumer0)
	consumer1FeePoolAddr := k.GetConsumerFeePoolAddress(consumer1)

	gomock.InOrder(
		mocks.MockBankKeeper.EXPECT().
			SendCoinsFromAccountToModule(gomock.Any(), consumer0FeePoolAddr, providertypes.ModuleName, sdk.NewCoins(feesPerBlock)).
			Return(sdkerrors.ErrInsufficientFunds.Wrapf("spendable 5 < 10")),
		mocks.MockBankKeeper.EXPECT().
			SendCoinsFromAccountToModule(gomock.Any(), consumer1FeePoolAddr, providertypes.ModuleName, sdk.NewCoins(feesPerBlock)).
			Return(nil),
	)

	total := k.CollectFeesFromConsumers(ctx)
	require.Equal(t, sdk.NewInt64Coin("uphoton", 10), total)
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
	providerParams := providertypes.DefaultParams()
	providerParams.FeesPerBlock = feesPerBlock
	k.SetParams(ctx, providerParams)
	consumer0FeePoolAddr := k.GetConsumerFeePoolAddress(consumer0)

	mocks.MockBankKeeper.EXPECT().
		SendCoinsFromAccountToModule(gomock.Any(), consumer0FeePoolAddr, providertypes.ModuleName, sdk.NewCoins(feesPerBlock)).
		Return(nil)

	total := k.CollectFeesFromConsumers(ctx)
	require.Equal(t, sdk.NewInt64Coin("uphoton", 10), total)
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
	providerParams := providertypes.DefaultParams()
	providerParams.FeesPerBlock = feesPerBlock
	k.SetParams(ctx, providerParams)
	consumer0FeePoolAddr := k.GetConsumerFeePoolAddress(consumer0)
	consumer1FeePoolAddr := k.GetConsumerFeePoolAddress(consumer1)

	gomock.InOrder(
		mocks.MockBankKeeper.EXPECT().
			SendCoinsFromAccountToModule(gomock.Any(), consumer0FeePoolAddr, providertypes.ModuleName, sdk.NewCoins(feesPerBlock)).
			Return(errors.New("bank send restriction")),
		mocks.MockBankKeeper.EXPECT().
			SendCoinsFromAccountToModule(gomock.Any(), consumer1FeePoolAddr, providertypes.ModuleName, sdk.NewCoins(feesPerBlock)).
			Return(nil),
	)

	total := k.CollectFeesFromConsumers(ctx)
	require.Equal(t, sdk.NewInt64Coin("uphoton", 10), total)
	require.False(t, k.IsConsumerInDebt(ctx, consumer0))
	require.False(t, k.IsConsumerInDebt(ctx, consumer1))
}

// Helpers for the distribution tests.

// newBondedValidator builds a Validator with a freshly generated consensus
// keypair so that val.GetConsAddr() returns a valid address. Returns the
// validator, its operator bytes (for sdk.AccAddress cast), and the consensus
// address to stamp into VoteInfos.
func newBondedValidator(t *testing.T, codec addresscodec.Codec, opSeed byte) (stakingtypes.Validator, []byte, sdk.ConsAddress) {
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
	return val, opBytes, sdk.GetConsAddress(pk)
}

func signerVote(consAddr sdk.ConsAddress) abci.VoteInfo {
	return abci.VoteInfo{
		Validator:   abci.Validator{Address: consAddr, Power: 1},
		BlockIdFlag: cmtproto.BlockIDFlagCommit,
	}
}

func absentVote(consAddr sdk.ConsAddress) abci.VoteInfo {
	return abci.VoteInfo{
		Validator:   abci.Validator{Address: consAddr, Power: 1},
		BlockIdFlag: cmtproto.BlockIDFlagAbsent,
	}
}

// TestDistributeFeesToValidators splits the pool evenly among signers,
// independent of their stake (tokens differ but shares are equal).
func TestDistributeFeesToValidators(t *testing.T) {
	params := testkeeper.NewInMemKeeperParams(t)
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, params)
	defer ctrl.Finish()

	valAddrCodec := address.NewBech32Codec("cosmosvaloper")
	mocks.MockStakingKeeper.EXPECT().ValidatorAddressCodec().Return(valAddrCodec).AnyTimes()

	val1, op1Bytes, cons1 := newBondedValidator(t, valAddrCodec, 1)
	val1.Tokens = sdk.DefaultPowerReduction.MulRaw(10) // stake differs on purpose
	val2, op2Bytes, cons2 := newBondedValidator(t, valAddrCodec, 2)
	val2.Tokens = sdk.DefaultPowerReduction.MulRaw(20)

	providerParams := providertypes.DefaultParams()
	providerParams.FeesPerBlock = sdk.NewInt64Coin("uphoton", 10)
	k.SetParams(ctx, providerParams)
	ctx = ctx.WithVoteInfos([]abci.VoteInfo{signerVote(cons1), signerVote(cons2)})

	mocks.MockBankKeeper.EXPECT().
		GetBalance(gomock.Any(), authtypes.NewModuleAddress(providertypes.ModuleName), providerParams.FeesPerBlock.Denom).
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
	providerParams.FeesPerBlock = sdk.NewInt64Coin("uphoton", 10)
	k.SetParams(ctx, providerParams)

	mocks.MockBankKeeper.EXPECT().
		GetBalance(gomock.Any(), authtypes.NewModuleAddress(providertypes.ModuleName), providerParams.FeesPerBlock.Denom).
		Return(sdk.NewCoin("uphoton", math.ZeroInt()))

	require.NoError(t, k.DistributeFeesToValidators(ctx))
}

// TestDistributeFeesToValidatorsSkipsWhenFeesTooSmall: pool holds 1 coin,
// 2 signers → floor(1/2) = 0 → nothing sent, balance stays pooled.
func TestDistributeFeesToValidatorsSkipsWhenFeesTooSmall(t *testing.T) {
	params := testkeeper.NewInMemKeeperParams(t)
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, params)
	defer ctrl.Finish()

	valAddrCodec := address.NewBech32Codec("cosmosvaloper")
	mocks.MockStakingKeeper.EXPECT().ValidatorAddressCodec().Return(valAddrCodec).AnyTimes()

	val1, _, cons1 := newBondedValidator(t, valAddrCodec, 1)
	val2, _, cons2 := newBondedValidator(t, valAddrCodec, 2)

	providerParams := providertypes.DefaultParams()
	providerParams.FeesPerBlock = sdk.NewInt64Coin("uphoton", 1)
	k.SetParams(ctx, providerParams)
	ctx = ctx.WithVoteInfos([]abci.VoteInfo{signerVote(cons1), signerVote(cons2)})

	mocks.MockBankKeeper.EXPECT().
		GetBalance(gomock.Any(), authtypes.NewModuleAddress(providertypes.ModuleName), providerParams.FeesPerBlock.Denom).
		Return(sdk.NewInt64Coin("uphoton", 1))
	mocks.MockStakingKeeper.EXPECT().
		GetBondedValidatorsByPower(gomock.Any()).
		Return([]stakingtypes.Validator{val1, val2}, nil)

	require.NoError(t, k.DistributeFeesToValidators(ctx))
}

// TestDistributeFeesToValidatorsRemainderStaysPooled: 10 coins / 3 signers
// → each gets floor(10/3) = 3, 1 coin stays in the pool.
func TestDistributeFeesToValidatorsRemainderStaysPooled(t *testing.T) {
	params := testkeeper.NewInMemKeeperParams(t)
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, params)
	defer ctrl.Finish()

	valAddrCodec := address.NewBech32Codec("cosmosvaloper")
	mocks.MockStakingKeeper.EXPECT().ValidatorAddressCodec().Return(valAddrCodec).AnyTimes()

	val1, op1, cons1 := newBondedValidator(t, valAddrCodec, 1)
	val2, op2, cons2 := newBondedValidator(t, valAddrCodec, 2)
	val3, op3, cons3 := newBondedValidator(t, valAddrCodec, 3)

	providerParams := providertypes.DefaultParams()
	providerParams.FeesPerBlock = sdk.NewInt64Coin("uphoton", 10)
	k.SetParams(ctx, providerParams)
	ctx = ctx.WithVoteInfos([]abci.VoteInfo{signerVote(cons1), signerVote(cons2), signerVote(cons3)})

	mocks.MockBankKeeper.EXPECT().
		GetBalance(gomock.Any(), authtypes.NewModuleAddress(providertypes.ModuleName), providerParams.FeesPerBlock.Denom).
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

// TestDistributeFeesToValidatorsNoSigners: empty VoteInfos → nothing sent.
func TestDistributeFeesToValidatorsNoSigners(t *testing.T) {
	params := testkeeper.NewInMemKeeperParams(t)
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, params)
	defer ctrl.Finish()

	providerParams := providertypes.DefaultParams()
	providerParams.FeesPerBlock = sdk.NewInt64Coin("uphoton", 50)
	k.SetParams(ctx, providerParams)

	mocks.MockBankKeeper.EXPECT().
		GetBalance(gomock.Any(), authtypes.NewModuleAddress(providertypes.ModuleName), providerParams.FeesPerBlock.Denom).
		Return(sdk.NewInt64Coin("uphoton", 50))
	mocks.MockStakingKeeper.EXPECT().
		GetBondedValidatorsByPower(gomock.Any()).
		Return(nil, nil)

	require.NoError(t, k.DistributeFeesToValidators(ctx))
}

// TestDistributeFeesToValidatorsSkipsAbsentSigners: absent / nil-voting
// validators are filtered out by BlockIdFlag; only commit votes earn.
func TestDistributeFeesToValidatorsSkipsAbsentSigners(t *testing.T) {
	params := testkeeper.NewInMemKeeperParams(t)
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, params)
	defer ctrl.Finish()

	valAddrCodec := address.NewBech32Codec("cosmosvaloper")
	mocks.MockStakingKeeper.EXPECT().ValidatorAddressCodec().Return(valAddrCodec).AnyTimes()

	val1, op1, cons1 := newBondedValidator(t, valAddrCodec, 1)
	val2, _, cons2 := newBondedValidator(t, valAddrCodec, 2)

	providerParams := providertypes.DefaultParams()
	providerParams.FeesPerBlock = sdk.NewInt64Coin("uphoton", 10)
	k.SetParams(ctx, providerParams)
	ctx = ctx.WithVoteInfos([]abci.VoteInfo{
		signerVote(cons1), // will be paid
		absentVote(cons2), // skipped — absent signer
	})

	mocks.MockBankKeeper.EXPECT().
		GetBalance(gomock.Any(), authtypes.NewModuleAddress(providertypes.ModuleName), providerParams.FeesPerBlock.Denom).
		Return(sdk.NewInt64Coin("uphoton", 100))
	mocks.MockStakingKeeper.EXPECT().
		GetBondedValidatorsByPower(gomock.Any()).
		Return([]stakingtypes.Validator{val1, val2}, nil)
	// Sole committing signer gets the entire pool.
	mocks.MockBankKeeper.EXPECT().
		SendCoinsFromModuleToAccount(gomock.Any(), providertypes.ModuleName, sdk.AccAddress(op1), sdk.NewCoins(sdk.NewInt64Coin("uphoton", 100))).
		Return(nil)

	require.NoError(t, k.DistributeFeesToValidators(ctx))
}

// TestDistributeFeesToValidatorsSkipsNonBondedSigner: a validator in VoteInfos
// whose consensus address is not in the current bonded set (because they were
// unbonded/removed since signing) forfeits its share.
func TestDistributeFeesToValidatorsSkipsNonBondedSigner(t *testing.T) {
	params := testkeeper.NewInMemKeeperParams(t)
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, params)
	defer ctrl.Finish()

	valAddrCodec := address.NewBech32Codec("cosmosvaloper")
	mocks.MockStakingKeeper.EXPECT().ValidatorAddressCodec().Return(valAddrCodec).AnyTimes()

	val1, op1, cons1 := newBondedValidator(t, valAddrCodec, 1)
	// val2 signed the previous block but is no longer in the bonded set.
	_, _, consGone := newBondedValidator(t, valAddrCodec, 2)

	providerParams := providertypes.DefaultParams()
	providerParams.FeesPerBlock = sdk.NewInt64Coin("uphoton", 10)
	k.SetParams(ctx, providerParams)
	ctx = ctx.WithVoteInfos([]abci.VoteInfo{signerVote(cons1), signerVote(consGone)})

	mocks.MockBankKeeper.EXPECT().
		GetBalance(gomock.Any(), authtypes.NewModuleAddress(providertypes.ModuleName), providerParams.FeesPerBlock.Denom).
		Return(sdk.NewInt64Coin("uphoton", 100))
	mocks.MockStakingKeeper.EXPECT().
		GetBondedValidatorsByPower(gomock.Any()).
		Return([]stakingtypes.Validator{val1}, nil) // val2 missing — unbonded since
	// Only the still-bonded signer is paid, and gets the full pool.
	mocks.MockBankKeeper.EXPECT().
		SendCoinsFromModuleToAccount(gomock.Any(), providertypes.ModuleName, sdk.AccAddress(op1), sdk.NewCoins(sdk.NewInt64Coin("uphoton", 100))).
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
	providerParams.FeesPerBlock = sdk.NewInt64Coin("uphoton", 10)
	k.SetParams(ctx, providerParams)

	mocks.MockBankKeeper.EXPECT().
		GetBalance(gomock.Any(), authtypes.NewModuleAddress(providertypes.ModuleName), providerParams.FeesPerBlock.Denom).
		Return(sdk.NewInt64Coin("uphoton", 100))
	mocks.MockStakingKeeper.EXPECT().
		GetBondedValidatorsByPower(gomock.Any()).
		Return(nil, errors.New("boom"))

	err := k.DistributeFeesToValidators(ctx)
	require.ErrorContains(t, err, "boom")
}
