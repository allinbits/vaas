package keeper_test

// track_historical_info_test.go covers Keeper.TrackHistoricalInfo, the
// per-block historical-entry writer the consumer runs in EndBlock. The getter
// and setter round trip is covered by TestHistoricalInfo in
// validators_test.go; what matters here is the retention behaviour: entries at
// or below height-HistoricalEntries are pruned, the current height is written
// with the cross-chain validator set, and HistoricalEntries=0 stores nothing.

import (
	"testing"

	"github.com/stretchr/testify/require"

	tmtypes "github.com/cometbft/cometbft/types"

	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	testkeeper "github.com/allinbits/vaas/testutil/keeper"
	vaastypes "github.com/allinbits/vaas/x/vaas/types"
)

// TestTrackHistoricalInfoPrunesAndStoresLatest sets HistoricalEntries to 5 at
// height 10: entries 1..5 (height - 5 and below) must be pruned, 6..9 kept,
// and the entry written at 10 must hold exactly the current cross-chain
// validator set.
func TestTrackHistoricalInfoPrunesAndStoresLatest(t *testing.T) {
	const (
		numHistoricalEntries = int64(5)
		blockHeight          = int64(10)
	)

	consumerKeeper, ctx, ctrl, _ := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	params := vaastypes.DefaultConsumerParams()
	params.HistoricalEntries = numHistoricalEntries
	consumerKeeper.SetParams(ctx, params)

	ctx = ctx.WithBlockHeight(blockHeight)

	validators := GenerateValidators(t)
	SetCCValidators(t, consumerKeeper, ctx, validators)

	// Seed a contiguous run of historical entries below the current height so
	// the pruning loop has something to walk (it stops at the first gap).
	for h := int64(1); h < blockHeight; h++ {
		hi := stakingtypes.NewHistoricalInfo(
			ctx.BlockHeader(),
			stakingtypes.Validators{ValidatorCodec: consumerKeeper.ValidatorAddressCodec()},
			sdk.DefaultPowerReduction,
		)
		consumerKeeper.SetHistoricalInfo(ctx, h, &hi)
	}

	require.NoError(t, consumerKeeper.TrackHistoricalInfo(ctx))

	for h := int64(1); h <= blockHeight-numHistoricalEntries; h++ {
		_, err := consumerKeeper.GetHistoricalInfo(ctx, h)
		require.ErrorIsf(t, err, stakingtypes.ErrNoHistoricalInfo,
			"historical entry at height %d should have been pruned", h)
	}
	for h := blockHeight - numHistoricalEntries + 1; h < blockHeight; h++ {
		_, err := consumerKeeper.GetHistoricalInfo(ctx, h)
		require.NoErrorf(t, err, "historical entry at height %d should have been retained", h)
	}

	latest, err := consumerKeeper.GetHistoricalInfo(ctx, blockHeight)
	require.NoError(t, err, "no historical entry written for the current height")
	require.Len(t, latest.Valset, len(validators))

	// The stored valset must be exactly the current cross-chain validators:
	// same consensus keys, and tokens derived from their voting power.
	wantTokens := make(map[string]string, len(validators))
	for _, v := range validators {
		wantTokens[v.PubKey.Address().String()] = sdk.TokensFromConsensusPower(v.VotingPower, sdk.DefaultPowerReduction).String()
	}
	for _, stored := range latest.Valset {
		pk, err := stored.ConsPubKey()
		require.NoError(t, err)
		want, ok := wantTokens[tmtypes.Address(pk.Address()).String()]
		require.Truef(t, ok, "historical entry holds unknown validator %X", pk.Address())
		require.Equalf(t, want, stored.Tokens.String(),
			"historical entry has wrong tokens for validator %X", pk.Address())
		require.Equal(t, stakingtypes.Bonded, stored.Status)
	}
}

// TestTrackHistoricalInfoStoresNothingWithZeroEntries pins the
// HistoricalEntries=0 contract: pruning still runs, but no entry is written
// for the current height.
func TestTrackHistoricalInfoStoresNothingWithZeroEntries(t *testing.T) {
	const blockHeight = int64(10)

	consumerKeeper, ctx, ctrl, _ := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	params := vaastypes.DefaultConsumerParams()
	params.HistoricalEntries = 0
	consumerKeeper.SetParams(ctx, params)

	ctx = ctx.WithBlockHeight(blockHeight)
	SetCCValidators(t, consumerKeeper, ctx, GenerateValidators(t))

	hi := stakingtypes.NewHistoricalInfo(
		ctx.BlockHeader(),
		stakingtypes.Validators{ValidatorCodec: consumerKeeper.ValidatorAddressCodec()},
		sdk.DefaultPowerReduction,
	)
	consumerKeeper.SetHistoricalInfo(ctx, blockHeight-1, &hi)

	require.NoError(t, consumerKeeper.TrackHistoricalInfo(ctx))

	_, err := consumerKeeper.GetHistoricalInfo(ctx, blockHeight)
	require.ErrorIs(t, err, stakingtypes.ErrNoHistoricalInfo,
		"no historical entry may be written when HistoricalEntries is zero")
}
