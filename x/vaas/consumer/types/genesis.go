package types

import (
	vaastypes "github.com/allinbits/vaas/x/vaas/types"

	abci "github.com/cometbft/cometbft/abci/types"

	ibctmtypes "github.com/cosmos/ibc-go/v10/modules/light-clients/07-tendermint"

	errorsmod "cosmossdk.io/errors"
)

// NewRestartGenesisState returns a consumer GenesisState that has already been established.
// Note: OutstandingDowntimeSlashing and LastTransmissionBlockHeight are deprecated
// as slash and distribution features have been removed.
func NewRestartGenesisState(
	clientID string,
	initValSet []abci.ValidatorUpdate,
	heightToValsetUpdateIDs []HeightToValsetUpdateID,
	params vaastypes.ConsumerParams,
) *GenesisState {
	return &GenesisState{
		NewChain: false,
		Params:   params,
		Provider: vaastypes.ProviderInfo{
			InitialValSet: initValSet,
		},
		HeightToValsetUpdateId: heightToValsetUpdateIDs,
		ProviderClientId:       clientID,
	}
}

// DefaultGenesisState returns a default disabled consumer chain genesis state. This allows the module to be hooked up to app without getting use
// unless explicitly specified in genesis.
func DefaultGenesisState() *GenesisState {
	return &GenesisState{
		Params: vaastypes.DefaultParams(),
	}
}

// NewInitialGenesisState returns a GenesisState for a completely new consumer chain.
func NewInitialGenesisState(cs *ibctmtypes.ClientState, consState *ibctmtypes.ConsensusState,
	initValSet []abci.ValidatorUpdate, params vaastypes.ConsumerParams,
) *GenesisState {
	return &GenesisState{
		NewChain: true,
		Params:   params,
		Provider: vaastypes.ProviderInfo{
			ClientState:    cs,
			ConsensusState: consState,
			InitialValSet:  initValSet,
		},
	}
}

// Validate performs basic genesis state validation returning an error upon any failure.
func (gs GenesisState) Validate() error {
	if !gs.Params.Enabled {
		return nil
	}
	if len(gs.Provider.InitialValSet) == 0 {
		return errorsmod.Wrap(vaastypes.ErrInvalidGenesis, "initial validator set is empty")
	}
	if err := gs.Params.Validate(); err != nil {
		return err
	}

	if gs.NewChain {
		if gs.Provider.ClientState == nil {
			return errorsmod.Wrap(vaastypes.ErrInvalidGenesis, "provider client state cannot be nil for new chain")
		}
		if err := gs.Provider.ClientState.Validate(); err != nil {
			return errorsmod.Wrapf(vaastypes.ErrInvalidGenesis, "provider client state invalid for new chain %s", err.Error())
		}
		if gs.Provider.ConsensusState == nil {
			return errorsmod.Wrap(vaastypes.ErrInvalidGenesis, "provider consensus state cannot be nil for new chain")
		}
		if err := gs.Provider.ConsensusState.ValidateBasic(); err != nil {
			return errorsmod.Wrapf(vaastypes.ErrInvalidGenesis, "provider consensus state invalid for new chain %s", err.Error())
		}

		if gs.ProviderClientId != "" {
			return errorsmod.Wrap(vaastypes.ErrInvalidGenesis, "provider client id cannot be set for new chain. It must be established on handshake")
		}
		if len(gs.HeightToValsetUpdateId) != 0 {
			return errorsmod.Wrap(vaastypes.ErrInvalidGenesis, "HeightToValsetUpdateId must be nil for new chain")
		}
	} else {
		if gs.ProviderClientId == "" {
			return errorsmod.Wrap(vaastypes.ErrInvalidGenesis, "provider client id must be set for a restarting consumer genesis state")
		}
		if gs.Provider.ClientState != nil || gs.Provider.ConsensusState != nil {
			return errorsmod.Wrap(vaastypes.ErrInvalidGenesis, "provider client state and consensus state must be nil for a restarting genesis state")
		}
	}
	return nil
}
