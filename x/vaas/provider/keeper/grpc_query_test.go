package keeper_test

import (
	"testing"
	"time"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	disttypes "github.com/cosmos/cosmos-sdk/x/distribution/types"

	testkeeper "github.com/allinbits/vaas/testutil/keeper"
	providertypes "github.com/allinbits/vaas/x/vaas/provider/types"
)

func TestQueryConsumerChainIncludesFeePoolAddress(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerId := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerChainId(ctx, consumerId, "consumer-chain")
	k.SetConsumerOwnerAddress(ctx, consumerId, "owner-address")
	k.SetConsumerPhase(ctx, consumerId, providertypes.CONSUMER_PHASE_REGISTERED)
	require.NoError(t, k.SetConsumerMetadata(ctx, consumerId, providertypes.ConsumerMetadata{
		Name: "name", Description: "description", Metadata: "metadata",
	}))

	mocks.MockSlashingKeeper.EXPECT().DowntimeJailDuration(gomock.Any()).Return(time.Hour, nil).AnyTimes()
	mocks.MockSlashingKeeper.EXPECT().SlashFractionDoubleSign(gomock.Any()).Return(math.LegacyNewDec(0), nil).AnyTimes()

	expected := k.GetConsumerFeePoolAddress(consumerId).String()

	res, err := k.QueryConsumerChain(ctx, &providertypes.QueryConsumerChainRequest{ConsumerId: consumerId})
	require.NoError(t, err)
	require.Equal(t, expected, res.FeePoolAddress)

	chain, err := k.GetConsumerChain(ctx, consumerId)
	require.NoError(t, err)
	require.Equal(t, expected, chain.FeePoolAddress)
}

func TestQueryConsumerFeePoolClaim(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerId := uint64(0)
	k.SetConsumerPhase(ctx, consumerId, providertypes.CONSUMER_PHASE_LAUNCHED)
	alice := sdk.AccAddress([]byte("alice___________"))
	poolAddr := k.GetConsumerFeePoolAddress(consumerId)
	require.NoError(t, k.ConsumerFeePoolShares.Set(ctx,
		collections.Join3(consumerId, "uphoton", alice), math.NewInt(50)))
	require.NoError(t, k.ConsumerFeePoolTotalShares.Set(ctx,
		collections.Join(consumerId, "uphoton"), math.NewInt(100)))
	mocks.MockBankKeeper.EXPECT().GetBalance(ctx, poolAddr, "uphoton").
		Return(sdk.NewInt64Coin("uphoton", 200))

	res, err := k.ConsumerFeePoolClaim(ctx, &providertypes.QueryConsumerFeePoolClaimRequest{
		ConsumerId: consumerId, Depositor: alice.String(),
	})
	require.NoError(t, err)
	require.Equal(t, "100uphoton", res.Claim.String())
}

func TestQueryConsumerFeePoolClaim_UnknownConsumer(t *testing.T) {
	k, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	alice := sdk.AccAddress([]byte("alice___________"))
	_, err := k.ConsumerFeePoolClaim(ctx, &providertypes.QueryConsumerFeePoolClaimRequest{
		ConsumerId: 999, Depositor: alice.String(),
	})
	require.Error(t, err)
	require.Equal(t, codes.NotFound, status.Code(err))
}

func TestQueryConsumerFeePoolClaim_GovAlias(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerId := uint64(0)
	k.SetConsumerPhase(ctx, consumerId, providertypes.CONSUMER_PHASE_LAUNCHED)
	distrAddr := authtypes.NewModuleAddress(disttypes.ModuleName)
	poolAddr := k.GetConsumerFeePoolAddress(consumerId)
	require.NoError(t, k.ConsumerFeePoolShares.Set(ctx,
		collections.Join3(consumerId, "uphoton", distrAddr), math.NewInt(100)))
	require.NoError(t, k.ConsumerFeePoolTotalShares.Set(ctx,
		collections.Join(consumerId, "uphoton"), math.NewInt(100)))
	mocks.MockBankKeeper.EXPECT().GetBalance(ctx, poolAddr, "uphoton").
		Return(sdk.NewInt64Coin("uphoton", 50))

	res, err := k.ConsumerFeePoolClaim(ctx, &providertypes.QueryConsumerFeePoolClaimRequest{
		ConsumerId: consumerId, Depositor: k.GetAuthority(),
	})
	require.NoError(t, err)
	require.Equal(t, "50uphoton", res.Claim.String())
}

func TestQueryConsumerFeePoolClaims(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerId := uint64(0)
	k.SetConsumerPhase(ctx, consumerId, providertypes.CONSUMER_PHASE_LAUNCHED)
	alice := sdk.AccAddress([]byte("alice___________"))
	bob := sdk.AccAddress([]byte("bob_____________"))
	poolAddr := k.GetConsumerFeePoolAddress(consumerId)

	require.NoError(t, k.ConsumerFeePoolShares.Set(ctx,
		collections.Join3(consumerId, "uphoton", alice), math.NewInt(30)))
	require.NoError(t, k.ConsumerFeePoolShares.Set(ctx,
		collections.Join3(consumerId, "uphoton", bob), math.NewInt(70)))
	require.NoError(t, k.ConsumerFeePoolTotalShares.Set(ctx,
		collections.Join(consumerId, "uphoton"), math.NewInt(100)))
	mocks.MockBankKeeper.EXPECT().GetBalance(ctx, poolAddr, "uphoton").
		Return(sdk.NewInt64Coin("uphoton", 100)).AnyTimes()

	res, err := k.ConsumerFeePoolClaims(ctx, &providertypes.QueryConsumerFeePoolClaimsRequest{
		ConsumerId: consumerId,
	})
	require.NoError(t, err)
	require.Len(t, res.Claims, 2)

	// Strong assertions: alice 30 shares × 100 / 100 = 30; bob 70 × 100 / 100 = 70.
	got := map[string]string{}
	for _, c := range res.Claims {
		got[c.Depositor] = c.Claim.String()
	}
	require.Equal(t, "30uphoton", got[alice.String()])
	require.Equal(t, "70uphoton", got[bob.String()])
}

func TestQueryConsumerFeePoolClaims_UnknownConsumer(t *testing.T) {
	k, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	_, err := k.ConsumerFeePoolClaims(ctx, &providertypes.QueryConsumerFeePoolClaimsRequest{
		ConsumerId: 999,
	})
	require.Error(t, err)
	require.Equal(t, codes.NotFound, status.Code(err))
}
