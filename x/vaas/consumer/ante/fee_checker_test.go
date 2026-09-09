package ante

import (
	"testing"

	"github.com/stretchr/testify/require"

	sdkmath "cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"
	channeltypesv2 "github.com/cosmos/ibc-go/v10/modules/core/04-channel/v2/types"
)

// TestInfrastructureExemptTxFeeChecker covers the consumer's TxFeeChecker: the
// node's minimum-gas-prices floor applies at CheckTx exactly as the SDK
// default, except that a transaction made up exclusively of infrastructure
// messages skips it. Without the skip, a validator raising its floor would
// price the relayer traffic the photon exemption exists to keep flowing.
func TestInfrastructureExemptTxFeeChecker(t *testing.T) {
	minPrices := sdk.NewDecCoins(sdk.NewDecCoinFromDec("uatone", sdkmath.LegacyMustNewDecFromStr("0.01")))
	checkCtx := sdk.Context{}.WithIsCheckTx(true).WithMinGasPrices(minPrices)
	deliverCtx := sdk.Context{}.WithMinGasPrices(minPrices)

	infra := []sdk.Msg{&channeltypesv2.MsgRecvPacket{}}
	user := []sdk.Msg{bankSendMsg()}

	testCases := []struct {
		name      string
		ctx       sdk.Context
		tx        mockFeeTx
		expectErr bool
		expectFee sdk.Coins
	}{
		{
			name: "infrastructure with no fee passes the floor at CheckTx",
			ctx:  checkCtx,
			tx:   mockFeeTx{msgs: infra, fee: sdk.NewCoins(), gas: 100000},
		},
		{
			name:      "user tx below the floor is rejected at CheckTx",
			ctx:       checkCtx,
			tx:        mockFeeTx{msgs: user, fee: sdk.NewCoins(), gas: 100000},
			expectErr: true,
		},
		{
			name: "user tx meeting the floor passes",
			ctx:  checkCtx,
			tx:   mockFeeTx{msgs: user, fee: sdk.NewCoins(sdk.NewInt64Coin("uatone", 1000)), gas: 100000},
		},
		{
			name:      "infrastructure keeps an attached fee for deduction",
			ctx:       checkCtx,
			tx:        mockFeeTx{msgs: infra, fee: sdk.NewCoins(sdk.NewInt64Coin("uatone", 500)), gas: 100000},
			expectFee: sdk.NewCoins(sdk.NewInt64Coin("uatone", 500)),
		},
		{
			name: "no floor outside CheckTx, for anyone",
			ctx:  deliverCtx,
			tx:   mockFeeTx{msgs: user, fee: sdk.NewCoins(), gas: 100000},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			fee, _, err := InfrastructureExemptTxFeeChecker(tc.ctx, tc.tx)
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
