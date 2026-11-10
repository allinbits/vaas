package app_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	abci "github.com/cometbft/cometbft/abci/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"

	dbm "github.com/cosmos/cosmos-db"

	"cosmossdk.io/log"

	"github.com/cosmos/cosmos-sdk/baseapp"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	simtestutil "github.com/cosmos/cosmos-sdk/testutil/sims"
	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	app "github.com/allinbits/vaas/app/provider"
)

// TestPreRotationEquivocationAfterRestart pins what the provider gets from
// the pinned atomone-sdk when a validator rotates its consensus key, the chain
// restarts from an export, and CometBFT then reports a double-sign committed
// under the old key, which it names by the old address:
//
//   - the block goes through: x/evidence no longer fails FinalizeBlock on
//     evidence it cannot attribute (atomone-sdk#138);
//   - the old address still resolves to the validator: x/staking exports and
//     re-imports the rotation state (atomone-sdk#140);
//   - the validator is NOT punished. x/slashing rebuilds its address-to-pubkey
//     relation at InitGenesis from the validators' current keys only, so the
//     old address has no pubkey after the restart and x/evidence ignores the
//     evidence ("expected public key for validator not found"). Before the
//     restart the relation exists and the same evidence is punished.
//
// The third point is a gap in atomone-sdk, not in VAAS: either x/slashing's
// InitGenesis also registers the pubkeys in x/staking's rotation history, or
// x/evidence takes the pubkey from the validator it resolved. When a pin
// carries that fix this test fails on its last assertions; flip them to
// require the punishment (jailed, tombstoned and slashed under the live
// address) and delete this paragraph.
func TestPreRotationEquivocationAfterRestart(t *testing.T) {
	const (
		numVals = 3
		chainID = "provider-test-1"
	)

	genesisTime := time.Now().UTC()
	providerApp, vals := newProviderAppWithBondedValidators(t, chainID, numVals, genesisTime)
	rotating := vals[0]

	// The committed genesis block, signed under the old key, is the infraction.
	infractionHeight := providerApp.LastBlockHeight()

	// Block 2 rotates the validator's consensus key. The staking handler records
	// the rotation under the block height it runs at, and staking applies it at
	// that block's EndBlock, so the handler runs on an uncached context at the
	// next height and the block is then finalized and committed.
	newKey := newConsensusKey(t)
	newConsAddr := sdk.ConsAddress(newKey.Address())
	rotationHeight := providerApp.LastBlockHeight() + 1
	rotationTime := genesisTime.Add(time.Second)
	pkAny, err := codectypes.NewAnyWithValue(newKey)
	require.NoError(t, err)
	rotate := &stakingtypes.MsgRotateConsPubKey{ValidatorAddress: rotating.valAddr.String(), NewPubkey: pkAny}
	handler := providerApp.MsgServiceRouter().Handler(rotate)
	require.NotNil(t, handler, "MsgRotateConsPubKey has no handler")
	_, err = handler(providerApp.NewUncachedContext(false, cmtproto.Header{Height: rotationHeight, ChainID: chainID, Time: rotationTime}), rotate)
	require.NoError(t, err)
	_, err = providerApp.FinalizeBlock(&abci.RequestFinalizeBlock{Height: rotationHeight, Time: rotationTime})
	require.NoError(t, err)
	_, err = providerApp.Commit()
	require.NoError(t, err)

	ctx := providerApp.NewContextLegacy(true, cmtproto.Header{Height: providerApp.LastBlockHeight()})
	_, err = providerApp.StakingKeeper.GetValidatorByConsAddr(ctx, newConsAddr)
	require.NoError(t, err, "the rotation must have been applied")
	_, err = providerApp.StakingKeeper.GetValidatorByConsAddr(ctx, rotating.consAddr)
	require.NoError(t, err, "the pre-rotation address must still resolve before the restart")
	_, err = providerApp.SlashingKeeper.GetPubkey(ctx, rotating.consAddr.Bytes())
	require.NoError(t, err, "before the restart slashing still knows the pre-rotation pubkey")

	// Export at height and start a fresh app from the export, as a coordinated
	// restart does.
	exported, err := providerApp.ExportAppStateAndValidators(false, nil, nil)
	require.NoError(t, err)

	restarted := app.New(log.NewNopLogger(), dbm.NewMemDB(), nil, true, simtestutil.EmptyAppOptions{}, baseapp.SetChainID(chainID))
	restartHeight := exported.Height + 1
	restartTime := rotationTime.Add(time.Second)
	_, err = restarted.InitChain(&abci.RequestInitChain{
		ChainId:         chainID,
		Time:            restartTime,
		Validators:      []abci.ValidatorUpdate{},
		ConsensusParams: simtestutil.DefaultConsensusParams,
		AppStateBytes:   exported.AppState,
		InitialHeight:   restartHeight,
	})
	require.NoError(t, err)
	_, err = restarted.FinalizeBlock(&abci.RequestFinalizeBlock{Height: restartHeight, Time: restartTime})
	require.NoError(t, err)
	_, err = restarted.Commit()
	require.NoError(t, err)

	ctx = restarted.NewContextLegacy(true, cmtproto.Header{Height: restarted.LastBlockHeight()})
	_, err = restarted.StakingKeeper.GetValidatorByConsAddr(ctx, rotating.consAddr)
	require.NoError(t, err, "the export must carry the rotation state, so the pre-rotation address resolves after the restart")
	require.True(t, restarted.SlashingKeeper.HasValidatorSigningInfo(ctx, rotating.consAddr),
		"signing info must resolve through the live address after the restart")
	_, err = restarted.SlashingKeeper.GetPubkey(ctx, rotating.consAddr.Bytes())
	require.Error(t, err, "the atomone-sdk gap: slashing does not rebuild the pre-rotation pubkey relation from an export")
	preVal, err := restarted.StakingKeeper.GetValidatorByConsAddr(ctx, newConsAddr)
	require.NoError(t, err)
	require.False(t, preVal.IsJailed())
	preTokens := preVal.Tokens

	// CometBFT reports the double-sign under the address that signed it: the
	// pre-rotation one. The block must go through whatever the SDK does with
	// the evidence.
	_, err = restarted.FinalizeBlock(&abci.RequestFinalizeBlock{
		Height: restarted.LastBlockHeight() + 1,
		Time:   restartTime.Add(time.Second),
		Misbehavior: []abci.Misbehavior{{
			Type:             abci.MisbehaviorType_DUPLICATE_VOTE,
			Validator:        abci.Validator{Address: rotating.consAddr.Bytes(), Power: 10},
			Height:           infractionHeight,
			Time:             genesisTime,
			TotalVotingPower: 10 * numVals,
		}},
	})
	require.NoError(t, err, "evidence under a pre-rotation address must not fail the block")
	_, err = restarted.Commit()
	require.NoError(t, err)

	// The gap, pinned: the evidence was ignored, not punished. Flip these when
	// the pinned atomone-sdk closes it (see the test comment).
	ctx = restarted.NewContextLegacy(true, cmtproto.Header{Height: restarted.LastBlockHeight()})
	postVal, err := restarted.StakingKeeper.GetValidatorByConsAddr(ctx, newConsAddr)
	require.NoError(t, err)
	require.False(t, postVal.IsJailed(), "pinned atomone-sdk gap: the pre-rotation double-sign is ignored after a restart")
	require.False(t, restarted.SlashingKeeper.IsTombstoned(ctx, newConsAddr))
	require.True(t, postVal.Tokens.Equal(preTokens))

	// The other validators are untouched either way.
	for _, other := range vals[1:] {
		val, err := restarted.StakingKeeper.GetValidatorByConsAddr(ctx, other.consAddr)
		require.NoError(t, err)
		require.False(t, val.IsJailed())
		require.False(t, restarted.SlashingKeeper.IsTombstoned(ctx, other.consAddr))
	}
}
