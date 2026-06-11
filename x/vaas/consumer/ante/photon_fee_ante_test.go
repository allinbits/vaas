package ante

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"
	protov2 "google.golang.org/protobuf/proto"

	consumertypes "github.com/allinbits/vaas/x/vaas/consumer/types"
)

// mockFeeTx is a minimal sdk.FeeTx for testing the photon fee decorator.
type mockFeeTx struct {
	fee sdk.Coins
}

func (m mockFeeTx) GetMsgs() []sdk.Msg                    { return nil }
func (m mockFeeTx) GetMsgsV2() ([]protov2.Message, error) { return nil, nil }
func (m mockFeeTx) GetGas() uint64                        { return 0 }
func (m mockFeeTx) GetFee() sdk.Coins                     { return m.fee }
func (m mockFeeTx) FeePayer() []byte                      { return nil }
func (m mockFeeTx) FeeGranter() []byte                    { return nil }

func runPhotonDecorator(t *testing.T, k mockConsumerKeeper, tx sdk.Tx) (bool, error) {
	t.Helper()
	decorator := NewPhotonFeeDecorator(k)
	nextCalled := false
	_, err := decorator.AnteHandle(sdk.Context{}, tx, false, func(ctx sdk.Context, tx sdk.Tx, simulate bool) (sdk.Context, error) {
		nextCalled = true
		return ctx, nil
	})
	return nextCalled, err
}

func TestPhotonFeeDecorator(t *testing.T) {
	photon := ExpectedPhotonDenom("07-tendermint-0")
	testCases := []struct {
		name           string
		providerClient bool
		tx             sdk.Tx
		expectErr      bool
	}{
		{
			name:           "provider client not established is a no-op, even for a non-photon fee",
			providerClient: false,
			tx:             mockFeeTx{fee: sdk.NewCoins(sdk.NewInt64Coin("uatone", 100))},
		},
		{
			name:           "fee in the photon voucher denom passes",
			providerClient: true,
			tx:             mockFeeTx{fee: sdk.NewCoins(sdk.NewInt64Coin(photon, 100))},
		},
		{
			name:           "fee in any other denom is rejected",
			providerClient: true,
			tx:             mockFeeTx{fee: sdk.NewCoins(sdk.NewInt64Coin("uatone", 100))},
			expectErr:      true,
		},
		{
			name:           "multi-coin fee containing a non-photon denom is rejected",
			providerClient: true,
			tx:             mockFeeTx{fee: sdk.NewCoins(sdk.NewInt64Coin(photon, 100), sdk.NewInt64Coin("uatone", 5))},
			expectErr:      true,
		},
		{
			name:           "empty fee passes (denom-only policy as minimum-fee is out of scope)",
			providerClient: true,
			tx:             mockFeeTx{fee: sdk.NewCoins()},
		},
		{
			name:           "non-FeeTx passes (nothing to check)",
			providerClient: true,
			tx:             mockTx{},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			nextCalled, err := runPhotonDecorator(t, mockConsumerKeeper{providerClientFound: tc.providerClient}, tc.tx)
			if tc.expectErr {
				require.Error(t, err)
				require.True(t, errorsmod.IsOf(err, consumertypes.ErrInvalidFeeDenom))
				require.False(t, nextCalled)
				return
			}
			require.NoError(t, err)
			require.True(t, nextCalled)
		})
	}
}

// Wire-format pinning: ExpectedPhotonDenom must equal the ICS-20 denom
// ibc/UPPERHEX(SHA256("transfer/<clientID>/uphoton")), computed here from a raw
// literal independent of the transfertypes helper.
func TestExpectedPhotonDenomMatchesICS20Format(t *testing.T) {
	const clientID = "07-tendermint-0"
	sum := sha256.Sum256([]byte("transfer/" + clientID + "/uphoton"))
	want := "ibc/" + strings.ToUpper(hex.EncodeToString(sum[:]))
	require.Equal(t, want, ExpectedPhotonDenom(clientID))
}
