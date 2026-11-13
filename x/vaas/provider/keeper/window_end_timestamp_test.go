package keeper_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	ibcclienttypes "github.com/cosmos/ibc-go/v10/modules/core/02-client/types"
	host "github.com/cosmos/ibc-go/v10/modules/core/24-host"
	ibctmtypes "github.com/cosmos/ibc-go/v10/modules/light-clients/07-tendermint"

	storetypes "cosmossdk.io/store/types"

	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/cosmos/cosmos-sdk/runtime"
	sdk "github.com/cosmos/cosmos-sdk/types"

	testkeeper "github.com/allinbits/vaas/testutil/keeper"
	providerkeeper "github.com/allinbits/vaas/x/vaas/provider/keeper"
)

// These tests exercise the real (non-overridden) windowEndTimestamp anchor
// resolution -- every other downtime test in this package instead overrides
// it via OverrideWindowEndTimestampForTest, bypassing the real IBC-client-
// backed lookup entirely (see setupDowntimeTest in
// consumer_equivocation_test.go). Here a real clienttypes.StoreProvider is
// built over the same in-memory KVStoreService the test keeper already uses
// -- the IBC client store's "clients/<clientID>/" byte prefix cannot collide
// with the vaas module's single-byte collection prefixes, so reusing the
// store is safe -- and seeded with real consensus states and their iteration
// keys, exactly what the 07-tendermint light client writes on an update, so
// the seeks run unmocked against the store ibc-go's own helpers read. Only
// the client state, which supplies the revision number, stays mocked.

func newFakeClientStoreProvider(params testkeeper.InMemKeeperParams) ibcclienttypes.StoreProvider {
	return ibcclienttypes.NewStoreProvider(runtime.NewKVStoreService(params.StoreKey))
}

const anchorClientId = "07-tendermint-0"

type anchorFixture struct {
	keeper      providerkeeper.Keeper
	ctx         sdk.Context
	clientStore storetypes.KVStore
	cdc         codec.Codec
	revision    uint64
}

func newAnchorFixture(t *testing.T, revision uint64) anchorFixture {
	t.Helper()
	keeperParams := testkeeper.NewInMemKeeperParams(t)
	providerKeeper, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, keeperParams)
	t.Cleanup(ctrl.Finish)

	storeProvider := newFakeClientStoreProvider(keeperParams)
	mocks.MockClientKeeper.EXPECT().GetStoreProvider().Return(storeProvider).AnyTimes()
	mocks.MockClientKeeper.EXPECT().GetClientState(gomock.Any(), anchorClientId).
		Return(&ibctmtypes.ClientState{LatestHeight: ibcclienttypes.NewHeight(revision, 1)}, true).AnyTimes()

	return anchorFixture{
		keeper:      providerKeeper,
		ctx:         ctx,
		clientStore: storeProvider.ClientStore(ctx, anchorClientId),
		cdc:         keeperParams.Cdc,
		revision:    revision,
	}
}

// seed stores a consensus state at height in the fixture's revision, stamped
// with stamp(height), under both the consensus-state key and the iteration
// key, as the 07-tendermint light client does on every update.
func (f anchorFixture) seed(heights ...uint64) {
	for _, h := range heights {
		height := ibcclienttypes.NewHeight(f.revision, h)
		consensusState := &ibctmtypes.ConsensusState{Timestamp: stamp(h)}
		f.clientStore.Set(host.ConsensusStateKey(height), ibcclienttypes.MustMarshalConsensusState(f.cdc, consensusState))
		ibctmtypes.SetIterationKey(f.clientStore, height)
	}
}

// anchor resolves windowEnd on a fresh gas meter and reports the gas the
// lookup consumed.
func (f anchorFixture) anchor(windowEnd int64) (time.Time, storetypes.Gas, error) {
	ctx := f.ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	got, err := f.keeper.WindowEndTimestampForTest(ctx, anchorClientId, windowEnd)
	return got, ctx.GasMeter().GasConsumed(), err
}

// stamp gives every height its own timestamp, so a test can tell which state
// anchored the window.
func stamp(height uint64) time.Time {
	return time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(height) * time.Minute)
}

func TestWindowEndTimestampSelectsExactHeightWhenStored(t *testing.T) {
	f := newAnchorFixture(t, 0)
	// Heights below, at, and above windowEnd=100. A state at exactly windowEnd
	// is both the highest at-or-below and the smallest at-or-above, so it is
	// the anchor -- 50 below it and 120/150 above it are all ignored.
	f.seed(50, 150, 120, 100)

	got, _, err := f.anchor(100)
	require.NoError(t, err)
	require.True(t, stamp(100).Equal(got), "expected %s, got %s", stamp(100), got)
}

func TestWindowEndTimestampErrorsWhenNoHeightAboveWindowEnd(t *testing.T) {
	f := newAnchorFixture(t, 0)
	// Every stored height is below windowEnd=100: no consensus state can
	// anchor the window, so this must error rather than pick a stale height.
	f.seed(10, 50, 99)

	_, _, err := f.anchor(100)
	require.Error(t, err)
}

func TestWindowEndTimestampErrorsOnEmptyClientStore(t *testing.T) {
	f := newAnchorFixture(t, 0)

	_, _, err := f.anchor(100)
	require.Error(t, err)
}

func TestWindowEndTimestampErrorsWithoutClientState(t *testing.T) {
	keeperParams := testkeeper.NewInMemKeeperParams(t)
	providerKeeper, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, keeperParams)
	defer ctrl.Finish()

	// The revision the lookup seeks in comes from the client state; without
	// one there is nothing to seek, whatever the store holds.
	mocks.MockClientKeeper.EXPECT().GetClientState(ctx, anchorClientId).Return(nil, false)

	_, err := providerKeeper.WindowEndTimestampForTest(ctx, anchorClientId, 100)
	require.ErrorContains(t, err, "no client state")
}

func TestWindowEndTimestampRejectsWhenWindowEndPredatesRetainedStates(t *testing.T) {
	f := newAnchorFixture(t, 0)
	// windowEnd=100, but every retained consensus state sits far above it: the
	// states at or below 100 have been pruned (or the client is sparse there),
	// so nothing supplies a verified lower bound on the height-100 time. Falling
	// back to the smallest state >= 100 (here 5000) would place the window end
	// far later than it really was, understating the evidence's age and letting
	// genuinely-stale evidence pass the age check. The anchor is rejected
	// instead.
	f.seed(5000, 6000)

	_, _, err := f.anchor(100)
	require.Error(t, err)
	require.ErrorContains(t, err, "retained consensus states")
}

func TestWindowEndTimestampAnchorsToHighestStateAtOrBelowWindowEnd(t *testing.T) {
	f := newAnchorFixture(t, 0)
	// windowEnd=100 sits between stored states. The state at 120 proves the
	// chain reached the window end, but the timestamp comes from 90 -- the
	// highest state at or below windowEnd, hence a verified lower bound on the
	// true height-100 time. Anchoring to 120 instead would place the window end
	// later than it really was and understate the evidence's age. 40 and 90 are
	// both at or below windowEnd; the higher one wins as the tighter bound.
	f.seed(40, 90, 120)

	got, _, err := f.anchor(100)
	require.NoError(t, err)
	require.True(t, stamp(90).Equal(got), "expected %s, got %s", stamp(90), got)
}

func TestWindowEndTimestampSeeksInTheClientRevision(t *testing.T) {
	// A client for a chain id like "consumer-3" carries a revision number of
	// 3 on every height (see clienttypes.ParseChainID), independent of the
	// revision height counter. The evidence names a bare height, so the seek
	// takes the revision from the client's latest height and a revision-3
	// state at 500 anchors windowEnd=500.
	f := newAnchorFixture(t, 3)
	f.seed(400, 500, 600)

	got, _, err := f.anchor(500)
	require.NoError(t, err)
	require.True(t, stamp(500).Equal(got), "expected %s, got %s", stamp(500), got)
}

// TestWindowEndTimestampIsUnchangedAndBoundedWithStatesOnBothSides: with
// thousands of consensus states on both sides of the window end, the anchor
// is still the highest state at or below it (or the state at it), and the
// lookup reads exactly what it reads on a client that retains only the states
// around the height: the gas consumed is the same, so a relayer feeding the
// client every consumer header cannot price evidence out of a block.
func TestWindowEndTimestampIsUnchangedAndBoundedWithStatesOnBothSides(t *testing.T) {
	const windowEnd = 1000

	manyAround := func(f anchorFixture, skip uint64) {
		for h := uint64(1); h <= 2*windowEnd; h++ {
			if h != skip {
				f.seed(h)
			}
		}
	}

	t.Run("window end between states", func(t *testing.T) {
		sparse := newAnchorFixture(t, 0)
		sparse.seed(windowEnd-1, windowEnd+1)
		dense := newAnchorFixture(t, 0)
		manyAround(dense, windowEnd)

		sparseGot, sparseGas, err := sparse.anchor(windowEnd)
		require.NoError(t, err)
		denseGot, denseGas, err := dense.anchor(windowEnd)
		require.NoError(t, err)

		require.True(t, stamp(windowEnd-1).Equal(sparseGot), "expected %s, got %s", stamp(windowEnd-1), sparseGot)
		require.True(t, sparseGot.Equal(denseGot), "expected %s, got %s", sparseGot, denseGot)
		require.Positive(t, sparseGas)
		require.Equal(t, sparseGas, denseGas, "the lookup read past the states around the window end")
	})

	t.Run("window end stored", func(t *testing.T) {
		sparse := newAnchorFixture(t, 0)
		sparse.seed(windowEnd)
		dense := newAnchorFixture(t, 0)
		manyAround(dense, 0)

		sparseGot, sparseGas, err := sparse.anchor(windowEnd)
		require.NoError(t, err)
		denseGot, denseGas, err := dense.anchor(windowEnd)
		require.NoError(t, err)

		require.True(t, stamp(windowEnd).Equal(sparseGot), "expected %s, got %s", stamp(windowEnd), sparseGot)
		require.True(t, sparseGot.Equal(denseGot), "expected %s, got %s", sparseGot, denseGot)
		require.Positive(t, sparseGas)
		require.Equal(t, sparseGas, denseGas, "the lookup read past the state at the window end")
	})
}
