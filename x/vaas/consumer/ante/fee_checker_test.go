package ante

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"

	transfertypes "github.com/cosmos/ibc-go/v10/modules/apps/transfer/types"
	clienttypes "github.com/cosmos/ibc-go/v10/modules/core/02-client/types"
	channeltypesv2 "github.com/cosmos/ibc-go/v10/modules/core/04-channel/v2/types"

	sdkmath "cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types/v1"

	consumertypes "github.com/allinbits/vaas/x/vaas/consumer/types"
)

// TestBootstrapExemptTxFeeChecker: the node fee floor is waived for bootstrap
// traffic while no routable provider client is pinned, and applied to
// everything, the relayer's deliveries included, once one is; on a
// photon-fees chain with no voucher at all, bootstrap traffic is waived again.
func TestBootstrapExemptTxFeeChecker(t *testing.T) {
	minPrices := sdk.NewDecCoins(sdk.NewDecCoinFromDec("uatone", sdkmath.LegacyMustNewDecFromStr("0.01")))
	checkCtx := sdk.Context{}.WithIsCheckTx(true).WithMinGasPrices(minPrices)
	deliverCtx := sdk.Context{}.WithMinGasPrices(minPrices)

	delivery := []sdk.Msg{&channeltypesv2.MsgRecvPacket{}}
	createClient := []sdk.Msg{&clienttypes.MsgCreateClient{}}
	pin := []sdk.Msg{&consumertypes.MsgSetProviderClient{}}
	gov := []sdk.Msg{&govtypes.MsgVote{}}
	user := []sdk.Msg{bankSendMsg()}
	noFee := sdk.NewCoins()
	atFloor := sdk.NewCoins(sdk.NewInt64Coin("uatone", 1000))

	testCases := []struct {
		name       string
		bootstrap  bool
		photonFees bool
		noVouchers bool
		ctx        sdk.Context
		tx        mockFeeTx
		expectErr bool
		expectFee sdk.Coins
	}{
		{name: "bootstrap: a delivery with no fee passes the floor", bootstrap: true, ctx: checkCtx, tx: mockFeeTx{msgs: delivery, fee: noFee, gas: 100000}},
		{name: "bootstrap: creating a client with no fee passes the floor", bootstrap: true, ctx: checkCtx, tx: mockFeeTx{msgs: createClient, fee: noFee, gas: 100000}},
		{name: "bootstrap: the pin with no fee passes the floor", bootstrap: true, ctx: checkCtx, tx: mockFeeTx{msgs: pin, fee: noFee, gas: 100000}},
		{name: "bootstrap: governance with no fee passes the floor", bootstrap: true, ctx: checkCtx, tx: mockFeeTx{msgs: gov, fee: noFee, gas: 100000}},
		{name: "bootstrap: a user tx below the floor is rejected", bootstrap: true, ctx: checkCtx, tx: mockFeeTx{msgs: user, fee: noFee, gas: 100000}, expectErr: true},
		{name: "bootstrap: a raw ICS-20 v2 send below the floor is rejected", bootstrap: true, ctx: checkCtx, tx: mockFeeTx{msgs: []sdk.Msg{&channeltypesv2.MsgSendPacket{}}, fee: noFee, gas: 100000}, expectErr: true},
		{name: "bootstrap: a transfer application message below the floor is rejected", bootstrap: true, ctx: checkCtx, tx: mockFeeTx{msgs: []sdk.Msg{&transfertypes.MsgTransfer{}}, fee: noFee, gas: 100000}, expectErr: true},
		{name: "pinned: a delivery with no fee is rejected at CheckTx", bootstrap: false, ctx: checkCtx, tx: mockFeeTx{msgs: delivery, fee: noFee, gas: 100000}, expectErr: true},
		{name: "pinned: creating a client with no fee is rejected at CheckTx", bootstrap: false, ctx: checkCtx, tx: mockFeeTx{msgs: createClient, fee: noFee, gas: 100000}, expectErr: true},
		{name: "pinned: governance with no fee is rejected at CheckTx", bootstrap: false, ctx: checkCtx, tx: mockFeeTx{msgs: gov, fee: noFee, gas: 100000}, expectErr: true},
		{name: "pinned: a delivery meeting the floor passes", bootstrap: false, ctx: checkCtx, tx: mockFeeTx{msgs: delivery, fee: atFloor, gas: 100000}, expectFee: atFloor},
		{name: "pinned: a user tx meeting the floor passes", bootstrap: false, ctx: checkCtx, tx: mockFeeTx{msgs: user, fee: atFloor, gas: 100000}, expectFee: atFloor},
		{name: "bootstrap: an attached fee is kept for deduction", bootstrap: true, ctx: checkCtx, tx: mockFeeTx{msgs: delivery, fee: sdk.NewCoins(sdk.NewInt64Coin("uatone", 500)), gas: 100000}, expectFee: sdk.NewCoins(sdk.NewInt64Coin("uatone", 500))},
		{name: "no floor outside CheckTx, for anyone", bootstrap: false, ctx: deliverCtx, tx: mockFeeTx{msgs: user, fee: noFee, gas: 100000}},
		{name: "no vouchers on a photon-fees chain: a delivery with no fee passes the floor", photonFees: true, noVouchers: true, ctx: checkCtx, tx: mockFeeTx{msgs: delivery, fee: noFee, gas: 100000}},
		{name: "no vouchers on a photon-fees chain: creating a client with no fee passes the floor", photonFees: true, noVouchers: true, ctx: checkCtx, tx: mockFeeTx{msgs: createClient, fee: noFee, gas: 100000}},
		{name: "no vouchers on a photon-fees chain: a user tx below the floor is rejected", photonFees: true, noVouchers: true, ctx: checkCtx, tx: mockFeeTx{msgs: user, fee: noFee, gas: 100000}, expectErr: true},
		{name: "vouchers on a photon-fees chain: a delivery with no fee is rejected", photonFees: true, ctx: checkCtx, tx: mockFeeTx{msgs: delivery, fee: noFee, gas: 100000}, expectErr: true},
		{name: "no vouchers with photon fees off: a delivery with no fee is rejected", noVouchers: true, ctx: checkCtx, tx: mockFeeTx{msgs: delivery, fee: noFee, gas: 100000}, expectErr: true},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			checker := NewBootstrapExemptTxFeeChecker(mockConsumerKeeper{
				providerClientFound: !tc.bootstrap,
				routableClient:      !tc.bootstrap,
				photonFees:          tc.photonFees,
				noVouchers:          tc.noVouchers,
			})
			fee, _, err := checker(tc.ctx, tc.tx)
			if tc.expectErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			if tc.expectFee != nil {
				require.Equal(t, tc.expectFee, fee)
			}
		})
	}
}

// TestTxPriority: the priority is the smallest per-denom gas price, capped at
// math.MaxInt64 for a price beyond int64 instead of panicking, and zero for
// a zero-gas transaction.
func TestTxPriority(t *testing.T) {
	huge, ok := sdkmath.NewIntFromString("100000000000000000000000")
	require.True(t, ok)

	require.Equal(t, int64(0), txPriority(sdk.NewCoins(sdk.NewInt64Coin("uatone", 1000)), 0))
	require.Equal(t, int64(0), txPriority(sdk.NewCoins(), 1000))
	require.Equal(t, int64(10), txPriority(sdk.NewCoins(sdk.NewInt64Coin("uatone", 10000)), 1000))
	require.Equal(t, int64(2), txPriority(sdk.NewCoins(sdk.NewInt64Coin("uatone", 10000), sdk.NewInt64Coin("uphoton", 2000)), 1000))
	require.Equal(t, int64(math.MaxInt64), txPriority(sdk.NewCoins(sdk.NewCoin("uatone", huge)), 1000))
	require.Equal(t, int64(10), txPriority(sdk.NewCoins(sdk.NewCoin("uatone", huge), sdk.NewInt64Coin("uphoton", 10000)), 1000))

	// The checker hands the capped priority back rather than failing.
	checker := NewBootstrapExemptTxFeeChecker(mockConsumerKeeper{providerClientFound: true, routableClient: true})
	_, priority, err := checker(sdk.Context{}, mockFeeTx{msgs: []sdk.Msg{bankSendMsg()}, fee: sdk.NewCoins(sdk.NewCoin("uatone", huge)), gas: 1000})
	require.NoError(t, err)
	require.Equal(t, int64(math.MaxInt64), priority)
}
