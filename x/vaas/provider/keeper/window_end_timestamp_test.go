package keeper_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	ibcclienttypes "github.com/cosmos/ibc-go/v10/modules/core/02-client/types"
	ibctmtypes "github.com/cosmos/ibc-go/v10/modules/light-clients/07-tendermint"

	"github.com/cosmos/cosmos-sdk/runtime"

	testkeeper "github.com/allinbits/vaas/testutil/keeper"
)

// These tests exercise the real (non-overridden) windowEndTimestamp anchor
// resolution -- every other downtime test in this package instead overrides
// it via OverrideWindowEndTimestampForTest, bypassing the real IBC-client-
// backed lookup entirely (see setupDowntimeTest in
// consumer_equivocation_test.go). Here a real clienttypes.StoreProvider is
// built over the same in-memory KVStoreService the test keeper already uses
// -- the IBC client store's "clients/<clientID>/" byte prefix cannot collide
// with the vaas module's single-byte collection prefixes, so reusing the
// store is safe -- and seeded with real consensus-state iteration keys, so
// IterateConsensusStateAscending's height-selection logic runs unmocked.
// Only GetClientConsensusState (which needs a decoded consensus state, not
// just an iteration key) stays mocked.

func newFakeClientStoreProvider(params testkeeper.InMemKeeperParams) ibcclienttypes.StoreProvider {
	return ibcclienttypes.NewStoreProvider(runtime.NewKVStoreService(params.StoreKey))
}

func TestWindowEndTimestampSelectsSmallestHeightAtOrAboveWindowEnd(t *testing.T) {
	keeperParams := testkeeper.NewInMemKeeperParams(t)
	providerKeeper, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, keeperParams)
	defer ctrl.Finish()

	const clientId = "07-tendermint-0"
	storeProvider := newFakeClientStoreProvider(keeperParams)
	mocks.MockClientKeeper.EXPECT().GetStoreProvider().Return(storeProvider).AnyTimes()

	clientStore := storeProvider.ClientStore(ctx, clientId)
	// Heights below, at, and above windowEnd=100; the smallest at-or-above
	// (100 itself) must be chosen over 120 and 150.
	for _, h := range []uint64{50, 150, 120, 100} {
		ibctmtypes.SetIterationKey(clientStore, ibcclienttypes.NewHeight(0, h))
	}

	wantHeight := ibcclienttypes.NewHeight(0, 100)
	wantTime := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	mocks.MockClientKeeper.EXPECT().
		GetClientConsensusState(ctx, clientId, wantHeight).
		Return(&ibctmtypes.ConsensusState{Timestamp: wantTime}, true)

	got, err := providerKeeper.WindowEndTimestampForTest(ctx, clientId, 100)
	require.NoError(t, err)
	require.True(t, wantTime.Equal(got), "expected %s, got %s", wantTime, got)
}

func TestWindowEndTimestampErrorsWhenNoHeightAboveWindowEnd(t *testing.T) {
	keeperParams := testkeeper.NewInMemKeeperParams(t)
	providerKeeper, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, keeperParams)
	defer ctrl.Finish()

	const clientId = "07-tendermint-0"
	storeProvider := newFakeClientStoreProvider(keeperParams)
	mocks.MockClientKeeper.EXPECT().GetStoreProvider().Return(storeProvider).AnyTimes()

	clientStore := storeProvider.ClientStore(ctx, clientId)
	// Every stored height is below windowEnd=100: no consensus state can
	// anchor the window, so this must error rather than pick a stale height.
	for _, h := range []uint64{10, 50, 99} {
		ibctmtypes.SetIterationKey(clientStore, ibcclienttypes.NewHeight(0, h))
	}

	_, err := providerKeeper.WindowEndTimestampForTest(ctx, clientId, 100)
	require.Error(t, err)
}

func TestWindowEndTimestampErrorsOnEmptyClientStore(t *testing.T) {
	keeperParams := testkeeper.NewInMemKeeperParams(t)
	providerKeeper, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, keeperParams)
	defer ctrl.Finish()

	storeProvider := newFakeClientStoreProvider(keeperParams)
	mocks.MockClientKeeper.EXPECT().GetStoreProvider().Return(storeProvider).AnyTimes()

	_, err := providerKeeper.WindowEndTimestampForTest(ctx, "07-tendermint-0", 100)
	require.Error(t, err)
}

func TestWindowEndTimestampIgnoresRevisionNumberSelectingByHeightOnly(t *testing.T) {
	keeperParams := testkeeper.NewInMemKeeperParams(t)
	providerKeeper, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, keeperParams)
	defer ctrl.Finish()

	// A client for a chain id like "consumer-3" carries a revision number of
	// 3 on every height (see clienttypes.ParseChainID), independent of the
	// revision height counter. windowEndTimestamp's selection only compares
	// GetRevisionHeight() against windowEnd, so a revision-3 height of 500
	// must still be selected as the anchor for windowEnd=500.
	const clientId = "07-tendermint-0"
	storeProvider := newFakeClientStoreProvider(keeperParams)
	mocks.MockClientKeeper.EXPECT().GetStoreProvider().Return(storeProvider).AnyTimes()

	clientStore := storeProvider.ClientStore(ctx, clientId)
	ibctmtypes.SetIterationKey(clientStore, ibcclienttypes.NewHeight(3, 400))
	ibctmtypes.SetIterationKey(clientStore, ibcclienttypes.NewHeight(3, 500))
	ibctmtypes.SetIterationKey(clientStore, ibcclienttypes.NewHeight(3, 600))

	wantHeight := ibcclienttypes.NewHeight(3, 500)
	wantTime := time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC)
	mocks.MockClientKeeper.EXPECT().
		GetClientConsensusState(ctx, clientId, wantHeight).
		Return(&ibctmtypes.ConsensusState{Timestamp: wantTime}, true)

	got, err := providerKeeper.WindowEndTimestampForTest(ctx, clientId, 500)
	require.NoError(t, err)
	require.True(t, wantTime.Equal(got), "expected %s, got %s", wantTime, got)
}
