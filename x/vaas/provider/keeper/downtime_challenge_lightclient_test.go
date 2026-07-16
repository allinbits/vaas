package keeper_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	tmproto "github.com/cometbft/cometbft/proto/tendermint/types"

	ibcclienttypes "github.com/cosmos/ibc-go/v10/modules/core/02-client/types"
	ibctmtypes "github.com/cosmos/ibc-go/v10/modules/light-clients/07-tendermint"

	testkeeper "github.com/allinbits/vaas/testutil/keeper"
)

// TestVerifyDowntimeChallengeHeaderDispatchesToLightClientModule exercises
// the real (non-overridden) production body of verifyDowntimeChallengeHeader
// -- every downtime challenge test in this package instead stubs it out via
// OverrideVerifyDowntimeChallengeHeaderForTest, so the
// NewLightClientModule + GetStoreProvider + VerifyClientMessage dispatch
// otherwise has zero test execution. A full happy-path light-client
// verification would require fabricating a real trusted validator set and
// consensus states (disproportionate here, per the final-review triage);
// instead this proves the wiring reaches the light client module by
// dispatching against a client id with no stored client state, which must
// fail cleanly with a typed "client not found" error rather than panicking.
func TestVerifyDowntimeChallengeHeaderDispatchesToLightClientModule(t *testing.T) {
	keeperParams := testkeeper.NewInMemKeeperParams(t)
	providerKeeper, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, keeperParams)
	defer ctrl.Finish()

	// Real store provider over the test keeper's in-memory KVStoreService
	// (same Task-1 pattern as window_end_timestamp_test.go), but nothing is
	// ever written to it: the client id below is never registered.
	storeProvider := newFakeClientStoreProvider(keeperParams)
	mocks.MockClientKeeper.EXPECT().GetStoreProvider().Return(storeProvider).AnyTimes()

	header := &ibctmtypes.Header{
		SignedHeader: &tmproto.SignedHeader{
			Header: &tmproto.Header{
				ChainID: "consumer-chain-0",
				Height:  101,
			},
			Commit: &tmproto.Commit{},
		},
	}

	var err error
	require.NotPanics(t, func() {
		err = providerKeeper.VerifyDowntimeChallengeHeaderForTest(ctx, "07-tendermint-0", header)
	})
	require.Error(t, err)
	require.ErrorIs(t, err, ibcclienttypes.ErrClientNotFound)
}
