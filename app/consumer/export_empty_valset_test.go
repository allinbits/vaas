package app

import (
	"testing"

	"github.com/stretchr/testify/require"

	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"

	"cosmossdk.io/log"

	dbm "github.com/cosmos/cosmos-db"
	"github.com/cosmos/cosmos-sdk/testutil/sims"
)

// TestGetValidatorSetToleratesEmptySet: a consumer that has not yet received a
// validator set from the provider has no cross-chain validators, and exporting
// it must succeed with an empty set rather than fail.
func TestGetValidatorSetToleratesEmptySet(t *testing.T) {
	app := New(log.NewNopLogger(), dbm.NewMemDB(), nil, true, sims.EmptyAppOptions{})
	ctx := app.NewContextLegacy(true, cmtproto.Header{Height: app.LastBlockHeight()})

	vals, err := app.GetValidatorSet(ctx)
	require.NoError(t, err, "exporting a consumer with no validators must not fail")
	require.Empty(t, vals)
}
