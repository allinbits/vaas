package keeper

import (
	"github.com/allinbits/vaas/x/vaas/consumer/types"

	abci "github.com/cometbft/cometbft/abci/types"

	ibchost "github.com/cosmos/ibc-go/v10/modules/core/exported"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

func (k Keeper) InitGenesis(ctx sdk.Context, state *types.GenesisState) []abci.ValidatorUpdate {
	// PreVAAS is true during the process of a standalone to consumer changeover.
	// At the PreVAAS point in the process, the standalone chain has just been upgraded to include
	// the consumer VAAS module, but the standalone staking keeper is still managing the validator set.
	// Once the provider validator set starts validating blocks, the consumer VAAS module
	// will take over proof of stake capabilities, but the standalone staking keeper will
	// stick around for slashing/jailing purposes.
	if state.PreVAAS {
		k.SetPreVAASTrue(ctx)
		k.MarkAsPrevStandaloneChain(ctx)
		k.SetInitialValSet(ctx, state.Provider.InitialValSet)
	}
	k.SetInitGenesisHeight(ctx, ctx.BlockHeight())

	k.SetParams(ctx, state.Params)
	if !state.Params.Enabled {
		return nil
	}

	if state.NewChain {
		clientStateBytes, err := state.Provider.ClientState.Marshal()
		if err != nil {
			panic(err)
		}
		consensusStateBytes, err := state.Provider.ConsensusState.Marshal()
		if err != nil {
			panic(err)
		}

		cid, err := k.clientKeeper.CreateClient(ctx, ibchost.Tendermint, clientStateBytes, consensusStateBytes)
		if err != nil {
			panic(err)
		}

		k.SetProviderClientID(ctx, cid)
		k.SetHeightValsetUpdateID(ctx, uint64(ctx.BlockHeight()), uint64(0))

		k.Logger(ctx).Info("create new provider chain client",
			"client id", cid,
		)
	} else {
		for _, h2v := range state.HeightToValsetUpdateId {
			k.SetHeightValsetUpdateID(ctx, h2v.Height, h2v.ValsetUpdateId)
		}
		k.SetProviderClientID(ctx, state.ProviderClientId)
	}

	if state.PreVAAS {
		return []abci.ValidatorUpdate{}
	}

	// populate cross chain validators states with initial valset
	k.ApplyCCValidatorChanges(ctx, state.Provider.InitialValSet)
	return state.Provider.InitialValSet
}

// ExportGenesis returns the CCV consumer module's exported genesis
func (k Keeper) ExportGenesis(ctx sdk.Context) (genesis *types.GenesisState) {
	params := k.GetConsumerParams(ctx)
	if !params.Enabled {
		return types.DefaultGenesisState()
	}

	// export the current validator set
	valset := k.MustGetCurrentValidatorsAsABCIUpdates(ctx)

	clientID, ok := k.GetProviderClientID(ctx)
	if !ok {
		return types.DefaultGenesisState()
	}

	genesis = types.NewRestartGenesisState(
		clientID,
		valset,
		k.GetAllHeightToValsetUpdateIDs(ctx),
		params,
	)

	return genesis
}
