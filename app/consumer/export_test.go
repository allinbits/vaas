package app

import (
	"testing"

	"github.com/stretchr/testify/require"

	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	tmtypes "github.com/cometbft/cometbft/types"

	"cosmossdk.io/log"

	dbm "github.com/cosmos/cosmos-db"
	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	"github.com/cosmos/cosmos-sdk/testutil/sims"

	consumertypes "github.com/allinbits/vaas/x/vaas/consumer/types"
)

// TestGetValidatorSetCarriesPubKeysAndReloads: the exported consumer genesis
// validator set must carry each validator's consensus pubkey. A nil PubKey
// serializes as "pub_key": null, and on reload CometBFT's
// GenesisDoc.ValidateAndComplete dereferences it and panics, so
// `consumer export` then `consumer start` was a broken round-trip.
func TestGetValidatorSetCarriesPubKeysAndReloads(t *testing.T) {
	app := New(log.NewNopLogger(), dbm.NewMemDB(), nil, true, sims.EmptyAppOptions{})
	ctx := app.NewContextLegacy(true, cmtproto.Header{Height: app.LastBlockHeight()})

	pk := ed25519.GenPrivKey().PubKey()
	cVal, err := consumertypes.NewCCValidator(pk.Address(), 10, pk)
	require.NoError(t, err)
	app.ConsumerKeeper.SetCCValidator(ctx, cVal)

	vals, err := app.GetValidatorSet(ctx)
	require.NoError(t, err)
	require.Len(t, vals, 1)
	require.NotNil(t, vals[0].PubKey, "exported genesis validator must carry a non-nil consensus pubkey")
	require.Equal(t, vals[0].PubKey.Address(), vals[0].Address)

	// The reload path: ValidateAndComplete dereferences each PubKey.
	genDoc := &tmtypes.GenesisDoc{ChainID: "consumer-test", Validators: vals}
	require.NoError(t, genDoc.ValidateAndComplete())
}
