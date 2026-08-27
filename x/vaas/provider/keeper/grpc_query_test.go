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
	providerkeeper "github.com/allinbits/vaas/x/vaas/provider/keeper"
	providertypes "github.com/allinbits/vaas/x/vaas/provider/types"
)

func TestQueryConsumerChainIncludesFeePoolAddress(t *testing.T) {
	k, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerId := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerChainId(ctx, consumerId, "consumer-chain")
	k.SetConsumerOwnerAddress(ctx, consumerId, "owner-address")
	k.SetConsumerPhase(ctx, consumerId, providertypes.CONSUMER_PHASE_REGISTERED)
	require.NoError(t, k.SetConsumerMetadata(ctx, consumerId, providertypes.ConsumerMetadata{
		Name: "name", Description: "description", Metadata: "metadata",
	}))

	expected := k.GetConsumerFeePoolAddress(consumerId).String()

	res, err := k.QueryConsumerChain(ctx, &providertypes.QueryConsumerChainRequest{ConsumerId: consumerId})
	require.NoError(t, err)
	require.Equal(t, expected, res.FeePoolAddress)

	chain, err := k.GetConsumerChain(ctx, consumerId)
	require.NoError(t, err)
	require.Equal(t, expected, chain.FeePoolAddress)
}

func TestQueryConsumerFeesPerBlock(t *testing.T) {
	defaultFees := sdk.NewInt64Coin("uphoton", 1000)

	// A zero-value math.Int (IsNil()) in overrideAmount means "no override is set".
	cases := []struct {
		name           string
		overrideAmount math.Int
		wantCoin       sdk.Coin
		wantIsOverride bool
	}{
		{
			name:           "no override returns default",
			wantCoin:       defaultFees,
			wantIsOverride: false,
		},
		{
			name:           "override returns the override amount",
			overrideAmount: math.NewInt(2500),
			wantCoin:       sdk.NewInt64Coin("uphoton", 2500),
			wantIsOverride: true,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			params := testkeeper.NewInMemKeeperParams(t)
			k, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, params)
			defer ctrl.Finish()

			providerParams := providertypes.DefaultParams()
			providerParams.FeesPerBlockAmount = defaultFees.Amount
			k.SetParams(ctx, providerParams)

			consumerId := k.FetchAndIncrementConsumerId(ctx)
			k.SetConsumerPhase(ctx, consumerId, providertypes.CONSUMER_PHASE_REGISTERED)

			if !tc.overrideAmount.IsNil() {
				require.NoError(t, k.ConsumerFeesPerBlockOverride.Set(ctx, consumerId, tc.overrideAmount))
			}

			res, err := k.QueryConsumerFeesPerBlock(ctx, &providertypes.QueryConsumerFeesPerBlockRequest{
				ConsumerId: consumerId,
			})
			require.NoError(t, err)
			require.Equal(t, tc.wantIsOverride, res.IsOverride)
			require.Equal(t, tc.wantCoin, res.FeesPerBlock)
		})
	}
}

func TestQueryConsumerFeesPerBlock_UnknownOrDeletedConsumer(t *testing.T) {
	cases := []struct {
		name  string
		setup func(k providerkeeper.Keeper, ctx sdk.Context) uint64
	}{
		{
			name: "unknown consumer",
			setup: func(_ providerkeeper.Keeper, _ sdk.Context) uint64 {
				return 999
			},
		},
		{
			name: "deleted consumer",
			setup: func(k providerkeeper.Keeper, ctx sdk.Context) uint64 {
				consumerId := k.FetchAndIncrementConsumerId(ctx)
				k.SetConsumerPhase(ctx, consumerId, providertypes.CONSUMER_PHASE_DELETED)
				return consumerId
			},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			params := testkeeper.NewInMemKeeperParams(t)
			k, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, params)
			defer ctrl.Finish()
			k.SetParams(ctx, providertypes.DefaultParams())

			consumerId := tc.setup(k, ctx)

			_, err := k.QueryConsumerFeesPerBlock(ctx, &providertypes.QueryConsumerFeesPerBlockRequest{
				ConsumerId: consumerId,
			})
			require.Error(t, err)
			require.Equal(t, codes.NotFound, status.Code(err))
		})
	}
}

func TestQueryAllConsumerFeesPerBlockOverrides(t *testing.T) {
	params := testkeeper.NewInMemKeeperParams(t)
	k, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, params)
	defer ctrl.Finish()

	a := k.FetchAndIncrementConsumerId(ctx)
	b := k.FetchAndIncrementConsumerId(ctx)
	c := k.FetchAndIncrementConsumerId(ctx)
	for _, id := range []uint64{a, b, c} {
		k.SetConsumerPhase(ctx, id, providertypes.CONSUMER_PHASE_REGISTERED)
	}
	require.NoError(t, k.ConsumerFeesPerBlockOverride.Set(ctx, a, math.NewInt(500)))
	require.NoError(t, k.ConsumerFeesPerBlockOverride.Set(ctx, c, math.NewInt(700)))

	res, err := k.QueryAllConsumerFeesPerBlockOverrides(ctx, &providertypes.QueryAllConsumerFeesPerBlockOverridesRequest{})
	require.NoError(t, err)
	require.Len(t, res.Overrides, 2)
	// Collection iteration is ordered by uint64 key ascending.
	require.Equal(t, a, res.Overrides[0].ConsumerId)
	require.Equal(t, "500", res.Overrides[0].Amount)
	require.Equal(t, c, res.Overrides[1].ConsumerId)
	require.Equal(t, "700", res.Overrides[1].Amount)
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

func TestQueryConsumerLiveness(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()
	mocks.MockStakingKeeper.EXPECT().UnbondingTime(gomock.Any()).Return(21*24*time.Hour, nil).AnyTimes()

	const cid = uint64(0)
	k.SetConsumerPhase(ctx, cid, providertypes.CONSUMER_PHASE_LAUNCHED)
	require.NoError(t, k.SetConsumerLastAckTime(ctx, cid, ctx.BlockTime()))

	resp, err := k.QueryConsumerLiveness(ctx, &providertypes.QueryConsumerLivenessRequest{ConsumerId: cid})
	require.NoError(t, err)
	require.False(t, resp.Degraded)
	require.Equal(t, ctx.BlockTime().Add(resp.GracePeriod).UTC(), resp.RemovalEta.UTC())
}

// TestQueryConsumerLiveness_DegradedFlag verifies that Degraded is true when
// the last ack time is more than grace/2 ago.
//
// The Degraded condition is: blockTime.After(lastAck + grace/2).
// We set lastAck = blockTime - grace/2 - 1ns so elapsed > grace/2 by 1ns.
func TestQueryConsumerLiveness_DegradedFlag(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	unbonding := 21 * 24 * time.Hour
	mocks.MockStakingKeeper.EXPECT().UnbondingTime(gomock.Any()).Return(unbonding, nil).AnyTimes()

	const cid = uint64(0)
	k.SetConsumerPhase(ctx, cid, providertypes.CONSUMER_PHASE_LAUNCHED)

	// Compute grace first so we can position lastAck precisely.
	grace, err := k.LivenessGracePeriod(ctx)
	require.NoError(t, err)

	// lastAck is grace/2 + 1ns before blockTime: blockTime.After(lastAck + grace/2) == true.
	lastAck := ctx.BlockTime().Add(-(grace/2 + time.Nanosecond))
	require.NoError(t, k.SetConsumerLastAckTime(ctx, cid, lastAck))

	resp, err := k.QueryConsumerLiveness(ctx, &providertypes.QueryConsumerLivenessRequest{ConsumerId: cid})
	require.NoError(t, err)
	require.True(t, resp.Degraded, "expected Degraded=true when lastAck is past the half-grace mark")
}

// TestQueryConsumerLiveness_UnknownConsumer verifies that a consumer whose phase
// is UNSPECIFIED (never registered) causes QueryConsumerLiveness to return a
// codes.InvalidArgument gRPC error.
func TestQueryConsumerLiveness_UnknownConsumer(t *testing.T) {
	k, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	// Consumer 999 was never registered; its phase is UNSPECIFIED.
	_, err := k.QueryConsumerLiveness(ctx, &providertypes.QueryConsumerLivenessRequest{ConsumerId: 999})
	require.Error(t, err)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

// TestQueryPendingDowntimeSlashes verifies that querying a consumer returns
// exactly its own pending downtime slashes, with the bitmap and slash tokens
// intact, and does not leak entries belonging to another consumer.
func TestQueryPendingDowntimeSlashes(t *testing.T) {
	k, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	const consumer1 = uint64(1)
	const consumer2 = uint64(2)
	k.SetConsumerPhase(ctx, consumer1, providertypes.CONSUMER_PHASE_LAUNCHED)
	k.SetConsumerPhase(ctx, consumer2, providertypes.CONSUMER_PHASE_LAUNCHED)

	maturesAt := ctx.BlockTime().Add(time.Hour).UTC()

	entryA := providertypes.PendingDowntimeSlash{
		ConsumerId:         consumer1,
		ProviderConsAddr:   []byte("provider-addr-validator-a"),
		WindowStartHeight:  100,
		Span:               200,
		MissedCount:        50,
		MissedBlocksBitmap: []byte{0xff, 0x00, 0xab},
		SlashTokens:        math.NewInt(1000),
		MaturesAt:          maturesAt,
	}
	entryB := providertypes.PendingDowntimeSlash{
		ConsumerId:         consumer1,
		ProviderConsAddr:   []byte("provider-addr-validator-b"),
		WindowStartHeight:  150,
		Span:               200,
		MissedCount:        75,
		MissedBlocksBitmap: []byte{0x0f, 0xf0},
		SlashTokens:        math.NewInt(2000),
		MaturesAt:          maturesAt,
	}
	entryC := providertypes.PendingDowntimeSlash{
		ConsumerId:         consumer2,
		ProviderConsAddr:   []byte("provider-addr-validator-c"),
		WindowStartHeight:  100,
		Span:               200,
		MissedCount:        50,
		MissedBlocksBitmap: []byte{0x11},
		SlashTokens:        math.NewInt(3000),
		MaturesAt:          maturesAt,
	}

	require.NoError(t, k.PendingDowntimeSlashes.Set(ctx, collections.Join3(consumer1, entryA.ProviderConsAddr, entryA.WindowStartHeight+entryA.Span-1), entryA))
	require.NoError(t, k.PendingDowntimeSlashes.Set(ctx, collections.Join3(consumer1, entryB.ProviderConsAddr, entryB.WindowStartHeight+entryB.Span-1), entryB))
	require.NoError(t, k.PendingDowntimeSlashes.Set(ctx, collections.Join3(consumer2, entryC.ProviderConsAddr, entryC.WindowStartHeight+entryC.Span-1), entryC))

	resp, err := k.QueryPendingDowntimeSlashes(ctx, &providertypes.QueryPendingDowntimeSlashesRequest{ConsumerId: consumer1})
	require.NoError(t, err)
	require.ElementsMatch(t, []providertypes.PendingDowntimeSlash{entryA, entryB}, resp.Slashes)
}

// TestQueryPendingDowntimeSlashes_UnknownConsumer verifies that querying an
// unregistered consumer id returns a codes.InvalidArgument gRPC error.
func TestQueryPendingDowntimeSlashes_UnknownConsumer(t *testing.T) {
	k, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	_, err := k.QueryPendingDowntimeSlashes(ctx, &providertypes.QueryPendingDowntimeSlashesRequest{ConsumerId: 999})
	require.Error(t, err)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

// TestQueryWithheldFeeRecords verifies that querying a consumer returns
// exactly its own withheld fee records and does not leak entries belonging
// to another consumer.
func TestQueryWithheldFeeRecords(t *testing.T) {
	k, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	const consumer1 = uint64(1)
	const consumer2 = uint64(2)
	k.SetConsumerPhase(ctx, consumer1, providertypes.CONSUMER_PHASE_LAUNCHED)
	k.SetConsumerPhase(ctx, consumer2, providertypes.CONSUMER_PHASE_LAUNCHED)

	expiresAt := ctx.BlockTime().Add(time.Hour).UTC()

	recordA := providertypes.WithheldFeeRecord{
		ConsumerId:       consumer1,
		ProviderConsAddr: []byte("provider-addr-validator-a"),
		Amount:           sdk.NewInt64Coin("uphoton", 50),
		ExpiresAt:        expiresAt,
	}
	recordB := providertypes.WithheldFeeRecord{
		ConsumerId:       consumer1,
		ProviderConsAddr: []byte("provider-addr-validator-b"),
		Amount:           sdk.NewInt64Coin("uphoton", 75),
		ExpiresAt:        expiresAt,
	}
	recordC := providertypes.WithheldFeeRecord{
		ConsumerId:       consumer2,
		ProviderConsAddr: []byte("provider-addr-validator-c"),
		Amount:           sdk.NewInt64Coin("uphoton", 100),
		ExpiresAt:        expiresAt,
	}

	require.NoError(t, k.WithheldFeeRecords.Set(ctx, collections.Join(consumer1, recordA.ProviderConsAddr), recordA))
	require.NoError(t, k.WithheldFeeRecords.Set(ctx, collections.Join(consumer1, recordB.ProviderConsAddr), recordB))
	require.NoError(t, k.WithheldFeeRecords.Set(ctx, collections.Join(consumer2, recordC.ProviderConsAddr), recordC))

	resp, err := k.QueryWithheldFeeRecords(ctx, &providertypes.QueryWithheldFeeRecordsRequest{ConsumerId: consumer1})
	require.NoError(t, err)
	require.ElementsMatch(t, []providertypes.WithheldFeeRecord{recordA, recordB}, resp.Records)
}

// TestQueryWithheldFeeRecords_UnknownConsumer verifies that querying an
// unregistered consumer id returns a codes.InvalidArgument gRPC error.
func TestQueryWithheldFeeRecords_UnknownConsumer(t *testing.T) {
	k, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	_, err := k.QueryWithheldFeeRecords(ctx, &providertypes.QueryWithheldFeeRecordsRequest{ConsumerId: 999})
	require.Error(t, err)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}
