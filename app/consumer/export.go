package app

import (
	"encoding/json"
	"fmt"

	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	tmtypes "github.com/cometbft/cometbft/types"

	servertypes "github.com/cosmos/cosmos-sdk/server/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	slashingtypes "github.com/cosmos/cosmos-sdk/x/slashing/types"
)

// ExportAppStateAndValidators implements the simapp app interface
// by exporting the state of the application
func (app *App) ExportAppStateAndValidators(
	forZeroHeight bool, jailAllowedAddrs, modulesToExport []string,
) (servertypes.ExportedApp, error) {
	// As if they could withdraw from the start of the next block.
	// The context must carry the last committed height: distribution's
	// delegation-reward recalculation replays validator slash events between
	// the delegation's creation height and ctx.BlockHeight(), so a zero
	// height skips every slash event and panics on any validator slashed
	// after the delegation was created.
	ctx := app.NewContextLegacy(true, cmtproto.Header{Height: app.LastBlockHeight()})

	// We export at last height + 1, because that's the height at which
	// Tendermint will start InitChain.
	height := app.LastBlockHeight() + 1
	if forZeroHeight {
		height = 0
		app.prepForZeroHeightGenesis(ctx, jailAllowedAddrs)
	}

	genState, err := app.MM.ExportGenesis(ctx, app.appCodec)
	if err != nil {
		return servertypes.ExportedApp{}, err
	}
	appState, err := json.MarshalIndent(genState, "", "  ")
	if err != nil {
		return servertypes.ExportedApp{}, err
	}

	validators, err := app.GetValidatorSet(ctx)
	if err != nil {
		return servertypes.ExportedApp{}, err
	}

	return servertypes.ExportedApp{
		AppState:        appState,
		Height:          height,
		Validators:      validators,
		ConsensusParams: app.BaseApp.GetConsensusParams(ctx),
	}, nil
}

// prepare for fresh start at zero height
// NOTE zero height genesis is a temporary feature which will be deprecated
//
//	in favour of export at a block height
func (app *App) prepForZeroHeightGenesis(ctx sdk.Context, jailAllowedAddrs []string) {
	// set context height to zero
	height := ctx.BlockHeight()
	ctx = ctx.WithBlockHeight(0)

	// reset context height
	ctx = ctx.WithBlockHeight(height)

	/* Handle slashing state. */

	// reset start height on signing infos
	var setErr error
	err := app.SlashingKeeper.IterateValidatorSigningInfos(
		ctx,
		func(addr sdk.ConsAddress, info slashingtypes.ValidatorSigningInfo) (stop bool) {
			info.StartHeight = 0
			if err := app.SlashingKeeper.SetValidatorSigningInfo(ctx, addr, info); err != nil {
				setErr = err
				return true
			}
			return false
		},
	)
	if setErr != nil {
		panic(setErr)
	}
	if err != nil {
		panic(err)
	}
}

// GetValidatorSet returns a slice of bonded validators.
func (app *App) GetValidatorSet(ctx sdk.Context) ([]tmtypes.GenesisValidator, error) {
	cVals := app.ConsumerKeeper.GetAllCCValidator(ctx)
	if len(cVals) == 0 {
		return nil, fmt.Errorf("empty validator set")
	}

	vals := []tmtypes.GenesisValidator{}
	for _, v := range cVals {
		vals = append(vals, tmtypes.GenesisValidator{Address: v.Address, Power: v.Power})
	}
	return vals, nil
}
