package keeper

import (
	"github.com/allinbits/vaas/x/vaas/consumer/types"

	abci "github.com/cometbft/cometbft/abci/types"

	conntypes "github.com/cosmos/ibc-go/v10/modules/core/03-connection/types"
	ibchost "github.com/cosmos/ibc-go/v10/modules/core/exported"

	errorsmod "cosmossdk.io/errors"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

// InitGenesis initializes the CCV consumer state and binds to PortID.
// The three states in which a consumer chain can start/restart:
//
//  1. A client to the provider was never created, i.e. a new consumer chain is started for the first time.
//  2. A consumer chain restarts after a client to the provider was created, but the CCV channel handshake is still in progress
//  3. A consumer chain restarts after the CCV channel handshake was completed.
//
// IBC v2 Note: In IBC v2 (Eureka), the channel handshake is replaced by RegisterCounterparty.
// States 2 and 3 above will be simplified to just "client exists" in IBC v2. The channel-based
// logic is kept for backward compatibility during the migration period.
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
	k.SetInitGenesisHeight(ctx, ctx.BlockHeight()) // Usually 0, but not the case for changeover chains

	k.SetParams(ctx, state.Params)
	if !state.Params.Enabled {
		return nil
	}

	if state.NewChain {
		var clientID string
		if state.ConnectionId == "" {
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
			clientID = cid

			k.Logger(ctx).Info("create new provider chain client",
				"client id", clientID,
			)
		} else {
			connectionEnd, found := k.connectionKeeper.GetConnection(ctx, state.ConnectionId)
			if !found {
				panic(errorsmod.Wrapf(conntypes.ErrConnectionNotFound, "could not find connection(%s)", state.ConnectionId))
			}
			clientID = connectionEnd.ClientId

			k.Logger(ctx).Info("use existing client and connection to provider chain",
				"client id", clientID,
				"connection id", state.ConnectionId,
			)
		}

		k.SetProviderClientID(ctx, clientID)
		k.SetHeightValsetUpdateID(ctx, uint64(ctx.BlockHeight()), uint64(0))

	} else {
		// chain restarts with the CCV channel established
		if state.ProviderChannelId != "" {
			// set provider channel ID
			k.SetProviderChannel(ctx, state.ProviderChannelId)
		}

		// set height to valset update id mapping
		for _, h2v := range state.HeightToValsetUpdateId {
			k.SetHeightValsetUpdateID(ctx, h2v.Height, h2v.ValsetUpdateId)
		}

		// set provider client id
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

	// export all the states created after a provider channel got established
	if channelID, ok := k.GetProviderChannel(ctx); ok {
		clientID, found := k.GetProviderClientID(ctx)
		if !found {
			// This should never happen
			panic("provider client does not exist although provider channel does exist")
		}

		genesis = types.NewRestartGenesisState(
			clientID,
			channelID,
			valset,
			k.GetAllHeightToValsetUpdateIDs(ctx),
			params,
		)
	} else {
		clientID, ok := k.GetProviderClientID(ctx)
		// if provider clientID and channelID don't exist on the consumer chain,
		// then CCV protocol is disabled for this chain return a default genesis state
		if !ok {
			return types.DefaultGenesisState()
		}

		// export client states into a new chain genesis
		genesis = types.NewRestartGenesisState(
			clientID,
			"",
			valset,
			k.GetAllHeightToValsetUpdateIDs(ctx),
			params,
		)
	}

	return genesis
}
