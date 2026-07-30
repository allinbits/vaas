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

func runPhotonDecorator(t *testing.T, k mockConsumerKeeper, tx sdk.Tx, simulate bool) (bool, error) {
	t.Helper()
	decorator := NewPhotonFeeDecorator(k)
	nextCalled := false
	_, err := decorator.AnteHandle(sdk.Context{}, tx, simulate, func(ctx sdk.Context, tx sdk.Tx, simulate bool) (sdk.Context, error) {
		nextCalled = true
		return ctx, nil
	})
	return nextCalled, err
}

// TestPhotonFeeDecorator covers the param gate and both decorator phases. With
// photon_fees_enabled false the decorator is a full no-op whatever the fee and
// whatever the pin. With it true, bootstrap (no pin at all, or a pin without a
// registered counterparty -- the genesis client) is still a full no-op: no
// photon voucher can exist yet, and rejecting the fee-less relayer traffic of
// that phase would prevent the first VSC from ever arriving. Enforcing
// (routable pin) accepts exactly non-empty, voucher-denominated fees.
func TestPhotonFeeDecorator(t *testing.T) {
	photon := ExpectedPhotonDenom("07-tendermint-0")
	testCases := []struct {
		name           string
		photonFees     bool
		providerClient bool
		routableClient bool
		simulate       bool
		tx             sdk.Tx
		expectErr      bool
	}{
		{
			name:           "param off: non-photon fee passes over a routable pin",
			photonFees:     false,
			providerClient: true,
			routableClient: true,
			tx:             mockFeeTx{fee: sdk.NewCoins(sdk.NewInt64Coin("uatone", 100))},
		},
		{
			name:           "param off: empty fee passes over a routable pin",
			photonFees:     false,
			providerClient: true,
			routableClient: true,
			tx:             mockFeeTx{fee: sdk.NewCoins()},
		},
		{
			name:           "param off: non-photon fee passes during bootstrap",
			photonFees:     false,
			providerClient: false,
			tx:             mockFeeTx{fee: sdk.NewCoins(sdk.NewInt64Coin("uatone", 100))},
		},
		{
			name:           "no provider client pinned is a no-op, even for a non-photon fee",
			photonFees:     true,
			providerClient: false,
			tx:             mockFeeTx{fee: sdk.NewCoins(sdk.NewInt64Coin("uatone", 100))},
		},
		{
			name:           "no provider client pinned is a no-op for an empty fee",
			photonFees:     true,
			providerClient: false,
			tx:             mockFeeTx{fee: sdk.NewCoins()},
		},
		{
			name:           "unroutable pin (genesis client) is a no-op, even for a non-photon fee",
			photonFees:     true,
			providerClient: true,
			routableClient: false,
			tx:             mockFeeTx{fee: sdk.NewCoins(sdk.NewInt64Coin("uatone", 100))},
		},
		{
			name:           "unroutable pin (genesis client) is a no-op for an empty fee",
			photonFees:     true,
			providerClient: true,
			routableClient: false,
			tx:             mockFeeTx{fee: sdk.NewCoins()},
		},
		{
			name:           "enforcing: fee in the photon voucher denom passes",
			photonFees:     true,
			providerClient: true,
			routableClient: true,
			tx:             mockFeeTx{fee: sdk.NewCoins(sdk.NewInt64Coin(photon, 100))},
		},
		{
			name:           "enforcing: fee in any other denom is rejected",
			photonFees:     true,
			providerClient: true,
			routableClient: true,
			tx:             mockFeeTx{fee: sdk.NewCoins(sdk.NewInt64Coin("uatone", 100))},
			expectErr:      true,
		},
		{
			name:           "enforcing: multi-coin fee containing a non-photon denom is rejected",
			photonFees:     true,
			providerClient: true,
			routableClient: true,
			tx:             mockFeeTx{fee: sdk.NewCoins(sdk.NewInt64Coin(photon, 100), sdk.NewInt64Coin("uatone", 5))},
			expectErr:      true,
		},
		{
			name:           "enforcing: empty fee is rejected (paying nothing is not paying in photon)",
			photonFees:     true,
			providerClient: true,
			routableClient: true,
			tx:             mockFeeTx{fee: sdk.NewCoins()},
			expectErr:      true,
		},
		{
			name:           "enforcing: empty fee passes in simulation (gas estimation precedes fee computation)",
			photonFees:     true,
			providerClient: true,
			routableClient: true,
			simulate:       true,
			tx:             mockFeeTx{fee: sdk.NewCoins()},
		},
		{
			name:           "enforcing: wrong denom is rejected even in simulation",
			photonFees:     true,
			providerClient: true,
			routableClient: true,
			simulate:       true,
			tx:             mockFeeTx{fee: sdk.NewCoins(sdk.NewInt64Coin("uatone", 100))},
			expectErr:      true,
		},
		{
			name:           "enforcing: non-FeeTx passes (nothing to check)",
			photonFees:     true,
			providerClient: true,
			routableClient: true,
			tx:             mockTx{},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			k := mockConsumerKeeper{
				providerClientFound: tc.providerClient,
				routableClient:      tc.routableClient,
				photonFees:          tc.photonFees,
			}
			nextCalled, err := runPhotonDecorator(t, k, tc.tx, tc.simulate)
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
