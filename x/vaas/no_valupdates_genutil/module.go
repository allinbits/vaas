// Package genutil wraps the Cosmos SDK x/genutil module for use on a VAAS
// provider chain. It is identical to x/genutil except that its InitGenesis
// returns no validator updates: the gentxs still create the validators in
// x/staking, but the genesis validator set handed to CometBFT comes from the
// VAAS provider module's InitGenesis, which builds it from the staking bonded
// set, since the module manager accepts validator updates from one module only.
package genutil

import (
	"encoding/json"

	abci "github.com/cometbft/cometbft/abci/types"

	"cosmossdk.io/core/genesis"

	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/module"
	"github.com/cosmos/cosmos-sdk/x/genutil"
	"github.com/cosmos/cosmos-sdk/x/genutil/types"
)

var (
	_ module.AppModuleGenesis = AppModule{}
	_ module.AppModuleBasic   = genutil.AppModuleBasic{}
)

// AppModule implements an application module for the genutil module.
type AppModule struct {
	genutil.AppModule

	stakingKeeper    types.StakingKeeper
	deliverTx        genesis.TxHandler
	txEncodingConfig client.TxEncodingConfig
}

// NewAppModule creates a new AppModule object
func NewAppModule(accountKeeper types.AccountKeeper,
	stakingKeeper types.StakingKeeper, deliverTx genesis.TxHandler,
	txEncodingConfig client.TxEncodingConfig,
) module.GenesisOnlyAppModule {
	genutilAppModule := genutil.NewAppModule(accountKeeper, stakingKeeper, deliverTx, txEncodingConfig)
	genutilAppModule.AppModuleGenesis = AppModule{
		AppModule:        genutilAppModule.AppModuleGenesis.(genutil.AppModule),
		stakingKeeper:    stakingKeeper,
		deliverTx:        deliverTx,
		txEncodingConfig: txEncodingConfig,
	}
	return genutilAppModule
}

// InitGenesis delegates the InitGenesis call to the underlying x/genutil module,
// however, it returns no validator updates as validator updates will be provided by the provider module.
func (am AppModule) InitGenesis(ctx sdk.Context, cdc codec.JSONCodec, data json.RawMessage) []abci.ValidatorUpdate {
	var genesisState types.GenesisState

	cdc.MustUnmarshalJSON(data, &genesisState)
	_, err := genutil.InitGenesis(ctx, am.stakingKeeper, am.deliverTx, genesisState, am.txEncodingConfig)
	if err != nil {
		panic(err)
	}

	return []abci.ValidatorUpdate{}
}
