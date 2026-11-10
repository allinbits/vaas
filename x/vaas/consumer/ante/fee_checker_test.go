package ante

import (
	"context"
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

type bootstrapState bool

func (b bootstrapState) HasRoutableProviderClient(context.Context) bool { return !bool(b) }

// TestBootstrapExemptTxFeeChecker: the node fee floor is waived for bootstrap
// traffic while no provider client is pinned, and applied to everything,
// the relayer's deliveries included, once one is.
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
		name      string
		bootstrap bool
		ctx       sdk.Context
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
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			checker := NewBootstrapExemptTxFeeChecker(bootstrapState(tc.bootstrap))
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
