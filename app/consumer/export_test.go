package app

import (
	"testing"

	"github.com/stretchr/testify/require"

	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	tmtypes "github.com/cometbft/cometbft/types"

	"cosmossdk.io/log"

	dbm "github.com/cosmos/cosmos-db"
	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	"github.com/cosmos/cosmos-sdk/testutil/sims"

	consumertypes "github.com/allinbits/vaas/x/vaas/consumer/types"
)

// TestGetValidatorSetCarriesPubKeysAndReloads is the M6 property: the exported
// consumer genesis validator set must carry each validator's consensus pubkey.
// A GenesisValidator with a nil PubKey serializes as "pub_key": null, and on
// reload CometBFT's GenesisDoc.ValidateAndComplete dereferences PubKey.Address()
// and panics -- so `consumer export` then `consumer start` was a broken
// round-trip. This test asserts GetValidatorSet sets the pubkeys and that the
// resulting validator set validates (the reload path) without panicking.
func TestGetValidatorSetCarriesPubKeysAndReloads(t *testing.T) {
	app := New(log.NewNopLogger(), dbm.NewMemDB(), nil, true, sims.EmptyAppOptions{})
	ctx := app.NewContextLegacy(true, cmtproto.Header{Height: app.LastBlockHeight()})

	// Seed two cross-chain validators, mirroring how ApplyCCValidatorChanges
	// stores them (address derived from the consensus pubkey).
	seeds := []struct {
		pk    cryptotypes.PubKey
		power int64
	}{
		{ed25519.GenPrivKey().PubKey(), 10},
		{ed25519.GenPrivKey().PubKey(), 5},
	}
	for _, s := range seeds {
		cVal, err := consumertypes.NewCCValidator(s.pk.Address(), s.power, s.pk)
		require.NoError(t, err)
		app.ConsumerKeeper.SetCCValidator(ctx, cVal)
	}

	vals, err := app.GetValidatorSet(ctx)
	require.NoError(t, err)
	require.Len(t, vals, 2)
	for _, v := range vals {
		require.NotNil(t, v.PubKey, "exported genesis validator must carry a non-nil consensus pubkey")
		require.Equal(t, v.PubKey.Address(), v.Address, "genesis validator address must match its pubkey")
	}

	// The exported set must survive CometBFT's reload validation, which
	// dereferences each PubKey (nil pubkeys panic here).
	genDoc := &tmtypes.GenesisDoc{ChainID: "consumer-test", Validators: vals}
	require.NotPanics(t, func() {
		require.NoError(t, genDoc.ValidateAndComplete())
	})
}
