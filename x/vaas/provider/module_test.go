package provider

import (
	"bytes"
	"errors"
	"testing"

	abci "github.com/cometbft/cometbft/abci/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"

	"cosmossdk.io/math"
	testkeeper "github.com/allinbits/vaas/testutil/keeper"
	providertypes "github.com/allinbits/vaas/x/vaas/provider/types"
	"github.com/cosmos/cosmos-sdk/codec/address"
	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

// TestBeginBlockCommitsDebtStateWhenDistributionFails verifies that when the
// distribution step errors, the cached fee-collection state (including the
// consumer-in-debt flag) is still committed — the two phases run in separate
// CacheContexts on purpose.
func TestBeginBlockCommitsDebtStateWhenDistributionFails(t *testing.T) {
	params := testkeeper.NewInMemKeeperParams(t)
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, params)
	defer ctrl.Finish()

	appModule := NewAppModule(&k)

	consumerInDebt := k.FetchAndIncrementConsumerId(ctx)
	consumerPaying := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, consumerInDebt, providertypes.CONSUMER_PHASE_LAUNCHED)
	k.SetConsumerPhase(ctx, consumerPaying, providertypes.CONSUMER_PHASE_LAUNCHED)

	providerParams := providertypes.DefaultParams()
	providerParams.FeesPerBlock = sdk.NewInt64Coin("uphoton", 10)
	k.SetParams(ctx, providerParams)

	consumerInDebtFeePoolAddr := k.GetConsumerFeePoolAddress(consumerInDebt)
	consumerPayingFeePoolAddr := k.GetConsumerFeePoolAddress(consumerPaying)

	// Prime one bonded signer with a real consensus key so GetConsAddr works;
	// the bank send during distribution will be the failure point.
	valAddrCodec := address.NewBech32Codec("cosmosvaloper")
	mocks.MockStakingKeeper.EXPECT().ValidatorAddressCodec().Return(valAddrCodec).AnyTimes()
	opBytes := bytes.Repeat([]byte{0xfe}, 20)
	op, err := valAddrCodec.BytesToString(opBytes)
	require.NoError(t, err)
	pk := ed25519.GenPrivKey().PubKey()
	val, err := stakingtypes.NewValidator(op, pk, stakingtypes.Description{})
	require.NoError(t, err)
	val.Status = stakingtypes.Bonded
	val.Tokens = sdk.DefaultPowerReduction
	val.DelegatorShares = math.LegacyNewDecFromInt(sdk.DefaultPowerReduction)

	ctx = ctx.WithVoteInfos([]abci.VoteInfo{
		{
			Validator:   abci.Validator{Address: sdk.GetConsAddress(pk), Power: 1},
			BlockIdFlag: cmtproto.BlockIDFlagCommit,
		},
	})

	// Collection phase.
	mocks.MockBankKeeper.EXPECT().
		GetBalance(gomock.Any(), consumerInDebtFeePoolAddr, providerParams.FeesPerBlock.Denom).
		Return(sdk.NewInt64Coin("uphoton", 5))
	mocks.MockBankKeeper.EXPECT().
		GetBalance(gomock.Any(), consumerPayingFeePoolAddr, providerParams.FeesPerBlock.Denom).
		Return(sdk.NewInt64Coin("uphoton", 20))
	mocks.MockBankKeeper.EXPECT().
		SendCoinsFromAccountToModule(gomock.Any(), consumerPayingFeePoolAddr, providertypes.ModuleName, sdk.NewCoins(providerParams.FeesPerBlock)).
		Return(nil)
	// Distribution phase: bank send errors out, rolls back distribution only.
	mocks.MockBankKeeper.EXPECT().
		GetBalance(gomock.Any(), authtypes.NewModuleAddress(providertypes.ModuleName), providerParams.FeesPerBlock.Denom).
		Return(providerParams.FeesPerBlock)
	mocks.MockStakingKeeper.EXPECT().
		GetBondedValidatorsByPower(gomock.Any()).
		Return([]stakingtypes.Validator{val}, nil)
	mocks.MockBankKeeper.EXPECT().
		SendCoinsFromModuleToAccount(gomock.Any(), providertypes.ModuleName, sdk.AccAddress(opBytes), sdk.NewCoins(providerParams.FeesPerBlock)).
		Return(errors.New("distribution boom"))

	require.NoError(t, appModule.BeginBlock(sdk.WrapSDKContext(ctx)))

	require.True(t, k.IsConsumerInDebt(ctx, consumerInDebt))
	require.False(t, k.IsConsumerInDebt(ctx, consumerPaying))
}
