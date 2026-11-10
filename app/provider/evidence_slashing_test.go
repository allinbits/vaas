package app_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	abci "github.com/cometbft/cometbft/abci/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

// TestProviderPunishesNativeEquivocation is the C1 regression test: it proves
// that the x/evidence module is wired into the provider app end to end, so a
// CometBFT-reported DuplicateVoteEvidence against a provider validator is
// slashed, jailed, and tombstoned.
//
// This is the strongest feasible in-process test: it drives the real
// provider app through ABCI FinalizeBlock with a Misbehavior entry, exactly as
// CometBFT delivers a double-sign. baseapp threads that Misbehavior into the
// block's CometInfo, the evidence module's BeginBlocker (now registered in the
// begin-block ordering) reads it, and punishes the validator via the slashing
// keeper. A full multi-node double-sign is only reachable in the Docker e2e;
// this exercises every in-process link of the wired path.
func TestProviderPunishesNativeEquivocation(t *testing.T) {
	const (
		numVals = 3
		chainID = "provider-test-1"
	)

	// The committed genesis block is referenced as the (past) infraction height.
	infractionTime := time.Now().UTC()
	providerApp, vals := newProviderAppWithBondedValidators(t, chainID, numVals, infractionTime)
	infractionHeight := providerApp.LastBlockHeight()
	consAddrs := make([]sdk.ConsAddress, 0, numVals)
	for _, val := range vals {
		consAddrs = append(consAddrs, val.consAddr)
	}

	// Sanity: the target validator is bonded, not jailed, not tombstoned.
	target := consAddrs[0]
	ctx := providerApp.NewContextLegacy(true, cmtproto.Header{Height: providerApp.LastBlockHeight()})
	preVal, err := providerApp.StakingKeeper.GetValidatorByConsAddr(ctx, target)
	require.NoError(t, err)
	require.False(t, preVal.IsJailed())
	require.False(t, providerApp.SlashingKeeper.IsTombstoned(ctx, target))
	preTokens := preVal.Tokens

	// Deliver a DuplicateVote misbehaviour for the target validator through the
	// same ABCI path CometBFT uses. The evidence BeginBlocker must consume it.
	_, err = providerApp.FinalizeBlock(&abci.RequestFinalizeBlock{
		Height: providerApp.LastBlockHeight() + 1,
		Time:   infractionTime.Add(time.Second),
		Misbehavior: []abci.Misbehavior{{
			Type:             abci.MisbehaviorType_DUPLICATE_VOTE,
			Validator:        abci.Validator{Address: target.Bytes(), Power: 10},
			Height:           infractionHeight,
			Time:             infractionTime,
			TotalVotingPower: 10 * numVals,
		}},
	})
	require.NoError(t, err)
	_, err = providerApp.Commit()
	require.NoError(t, err)

	// The wired evidence path must have slashed, jailed, and tombstoned it.
	ctx = providerApp.NewContextLegacy(true, cmtproto.Header{Height: providerApp.LastBlockHeight()})
	postVal, err := providerApp.StakingKeeper.GetValidatorByConsAddr(ctx, target)
	require.NoError(t, err)
	require.True(t, postVal.IsJailed(), "double-signing validator must be jailed")
	require.True(t, providerApp.SlashingKeeper.IsTombstoned(ctx, target), "double-signing validator must be tombstoned")
	require.True(t, postVal.Tokens.LT(preTokens),
		"double-signing validator must be slashed: pre=%s post=%s", preTokens, postVal.Tokens)

	// A non-offending validator must be untouched.
	other, err := providerApp.StakingKeeper.GetValidatorByConsAddr(ctx, consAddrs[1])
	require.NoError(t, err)
	require.False(t, other.IsJailed(), "non-offending validator must not be jailed")
	require.False(t, providerApp.SlashingKeeper.IsTombstoned(ctx, consAddrs[1]))
}
