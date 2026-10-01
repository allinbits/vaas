package app

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	abci "github.com/cometbft/cometbft/abci/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	cmttypes "github.com/cometbft/cometbft/types"

	"cosmossdk.io/log"
	"cosmossdk.io/math"

	dbm "github.com/cosmos/cosmos-db"
	"github.com/cosmos/cosmos-sdk/baseapp"
	"github.com/cosmos/cosmos-sdk/client/flags"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"github.com/cosmos/cosmos-sdk/testutil/sims"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	slashingtypes "github.com/cosmos/cosmos-sdk/x/slashing/types"
)

const exportTestChainID = "vaas-provider-export-test"

type testAppOptions map[string]any

func (o testAppOptions) Get(key string) any { return o[key] }

// TestExportForZeroHeightWithdrawsRewardsAndKeepsValidators drives the
// `--for-zero-height` export through a chain with one bonded validator whose
// self-delegation has accrued block rewards and whose accumulated commission
// is zero, the state of any validator that runs at a zero commission rate.
// The export must succeed, carry the validator, and hand the accrued rewards
// to the delegator before the state is re-based to height zero.
func TestExportForZeroHeightWithdrawsRewardsAndKeepsValidators(t *testing.T) {
	papp := New(log.NewNopLogger(), dbm.NewMemDB(), nil, true,
		testAppOptions{flags.FlagHome: t.TempDir()}, baseapp.SetChainID(exportTestChainID))

	consPrivKey := ed25519.GenPrivKey()
	cmtPubKey, err := cryptocodec.ToCmtPubKeyInterface(consPrivKey.PubKey())
	require.NoError(t, err)
	valSet := cmttypes.NewValidatorSet([]*cmttypes.Validator{cmttypes.NewValidator(cmtPubKey, 1)})

	delegatorKey := secp256k1.GenPrivKey()
	delegator := sdk.AccAddress(delegatorKey.PubKey().Address())
	initialBalance := sdk.NewCoins(sdk.NewCoin(sdk.DefaultBondDenom, math.NewInt(1_000_000_000)))
	genesisState, err := sims.GenesisStateWithValSet(
		papp.AppCodec(), ModuleBasics.DefaultGenesis(papp.AppCodec()), valSet,
		[]authtypes.GenesisAccount{authtypes.NewBaseAccount(delegator, delegatorKey.PubKey(), 0, 0)},
		banktypes.Balance{Address: delegator.String(), Coins: initialBalance},
	)
	require.NoError(t, err)
	// Slashing creates signing info when a validator bonds through staking; a
	// genesis validator is born bonded, so its signing info has to be in the
	// genesis too or the first signed block fails in slashing's BeginBlock.
	consAddr := sdk.ConsAddress(cmtPubKey.Address())
	slashingGenesis := slashingtypes.NewGenesisState(slashingtypes.DefaultParams(), []slashingtypes.SigningInfo{{
		Address:              consAddr.String(),
		ValidatorSigningInfo: slashingtypes.NewValidatorSigningInfo(consAddr, 0, 0, time.Unix(0, 0).UTC(), false, 0),
	}}, nil)
	genesisState[slashingtypes.ModuleName] = papp.AppCodec().MustMarshalJSON(slashingGenesis)
	stateBytes, err := json.Marshal(genesisState)
	require.NoError(t, err)

	genesisTime := time.Unix(1_850_000_000, 0).UTC()
	_, err = papp.InitChain(&abci.RequestInitChain{
		ChainId:         exportTestChainID,
		Time:            genesisTime,
		ConsensusParams: sims.DefaultConsensusParams,
		AppStateBytes:   stateBytes,
		InitialHeight:   1,
	})
	require.NoError(t, err)

	// Three blocks signed by the validator: block provisions reach the fee
	// collector and distribution allocates them to the only voter, so the
	// self-delegation has rewards to withdraw and the (zero-rate) commission
	// stays empty.
	votes := []abci.VoteInfo{{
		Validator:   abci.Validator{Address: cmtPubKey.Address(), Power: 1},
		BlockIdFlag: cmtproto.BlockIDFlagCommit,
	}}
	for height := int64(1); height <= 3; height++ {
		_, err = papp.FinalizeBlock(&abci.RequestFinalizeBlock{
			Height:            height,
			Time:              genesisTime.Add(time.Duration(height) * 5 * time.Second),
			ProposerAddress:   cmtPubKey.Address(),
			DecidedLastCommit: abci.CommitInfo{Votes: votes},
		})
		require.NoError(t, err)
		_, err = papp.Commit()
		require.NoError(t, err)
	}

	exported, err := papp.ExportAppStateAndValidators(true, nil, nil)
	require.NoError(t, err)
	require.Zero(t, exported.Height, "a zero-height export restarts the chain at height 0")
	require.Len(t, exported.Validators, 1, "the bonded validator must survive the export")
	require.Equal(t, cmtPubKey.Address().Bytes(), exported.Validators[0].Address.Bytes())

	// The delegator holds its initial balance plus the withdrawn rewards in
	// the exported bank state.
	var appState map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(exported.AppState, &appState))
	var bankGenesis banktypes.GenesisState
	papp.AppCodec().MustUnmarshalJSON(appState[banktypes.ModuleName], &bankGenesis)
	var exportedBalance sdk.Coins
	for _, b := range bankGenesis.Balances {
		if b.Address == delegator.String() {
			exportedBalance = b.Coins
		}
	}
	require.True(t, exportedBalance.AmountOf(sdk.DefaultBondDenom).GT(initialBalance.AmountOf(sdk.DefaultBondDenom)),
		"the export must withdraw the accrued delegation rewards: have %s, started with %s", exportedBalance, initialBalance)
}
