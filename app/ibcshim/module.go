// Package ibcshim wraps ibc-go's core AppModule to work around a genesis
// re-import defect in ibc-go (present in v10.2 through at least v11.2, filed
// as cosmos/ibc-go#9063): clientv2's GenesisState.Validate rejects a
// counterparty entry whose counterparty client id equals the local client
// id. Those two identifiers live in different chains' namespaces, and
// equality is the normal case for two fresh chains whose first clients are
// both "07-tendermint-0". ibc-go's own MsgRegisterCounterparty creates
// exactly that state without complaint, so a chain that exports its genesis
// cannot re-import it: InitChain panics with "counterparty client id and
// client id cannot be the same", `genesis validate` fails on the export, and
// any restart from export is impossible.
//
// The wrapper re-implements clientv2's step of InitGenesis and of
// ValidateGenesis (the CLI's `genesis validate`), keeping every check except
// the cross-chain id comparison. Delete this package once the check is
// removed in ibc-go.
package ibcshim

import (
	"encoding/json"
	"errors"
	"fmt"

	abci "github.com/cometbft/cometbft/abci/types"

	ibc "github.com/cosmos/ibc-go/v10/modules/core"
	client "github.com/cosmos/ibc-go/v10/modules/core/02-client"
	clientv2types "github.com/cosmos/ibc-go/v10/modules/core/02-client/v2/types"
	connection "github.com/cosmos/ibc-go/v10/modules/core/03-connection"
	channel "github.com/cosmos/ibc-go/v10/modules/core/04-channel"
	channelv2 "github.com/cosmos/ibc-go/v10/modules/core/04-channel/v2"
	ibcexported "github.com/cosmos/ibc-go/v10/modules/core/exported"
	ibckeeper "github.com/cosmos/ibc-go/v10/modules/core/keeper"
	ibctypes "github.com/cosmos/ibc-go/v10/modules/core/types"

	sdkclient "github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"
)

// AppModule is ibc-go's core AppModule with InitGenesis and ValidateGenesis overridden.
type AppModule struct {
	ibc.AppModule
	keeper *ibckeeper.Keeper
}

// NewAppModule wraps ibc.NewAppModule.
func NewAppModule(k *ibckeeper.Keeper) AppModule {
	return AppModule{AppModule: ibc.NewAppModule(k), keeper: k}
}

// InitGenesis mirrors ibc-go's core InitGenesis submodule by submodule,
// replacing only clientv2's step (see the package comment).
func (am AppModule) InitGenesis(ctx sdk.Context, cdc codec.JSONCodec, bz json.RawMessage) []abci.ValidatorUpdate {
	var gs ibctypes.GenesisState
	if err := cdc.UnmarshalJSON(bz, &gs); err != nil {
		panic(fmt.Errorf("failed to unmarshal %s genesis state: %w", ibcexported.ModuleName, err))
	}

	client.InitGenesis(ctx, am.keeper.ClientKeeper, gs.ClientGenesis)
	initClientV2Genesis(ctx, am.keeper.ClientV2Keeper, gs.ClientV2Genesis)
	connection.InitGenesis(ctx, am.keeper.ConnectionKeeper, gs.ConnectionGenesis)
	channel.InitGenesis(ctx, am.keeper.ChannelKeeper, gs.ChannelGenesis)
	channelv2.InitGenesis(ctx, am.keeper.ChannelKeeperV2, gs.ChannelV2Genesis)
	return nil
}

// ValidateGenesis mirrors ibc-go's core ValidateGenesis (what the CLI's
// `genesis validate` runs) submodule by submodule, replacing only clientv2's
// step, so an exported genesis that InitGenesis accepts also validates.
func (AppModule) ValidateGenesis(cdc codec.JSONCodec, _ sdkclient.TxEncodingConfig, bz json.RawMessage) error {
	var gs ibctypes.GenesisState
	if err := cdc.UnmarshalJSON(bz, &gs); err != nil {
		return fmt.Errorf("failed to unmarshal %s genesis state: %w", ibcexported.ModuleName, err)
	}
	return validateGenesis(gs)
}

// validateGenesis is ibctypes.GenesisState.Validate with clientv2's step
// replaced by validateClientV2Genesis.
func validateGenesis(gs ibctypes.GenesisState) error {
	if err := gs.ClientGenesis.Validate(); err != nil {
		return err
	}
	if err := validateClientV2Genesis(gs.ClientV2Genesis); err != nil {
		return err
	}
	if err := gs.ConnectionGenesis.Validate(); err != nil {
		return err
	}
	if err := gs.ChannelGenesis.Validate(); err != nil {
		return err
	}
	return gs.ChannelV2Genesis.Validate()
}

// counterpartySetter is the one write initClientV2Genesis performs, factored
// so the workaround's behavior is testable without a full IBC keeper.
type counterpartySetter interface {
	SetClientCounterparty(ctx sdk.Context, clientID string, counterparty clientv2types.CounterpartyInfo)
}

// validateClientV2Genesis is clientv2's GenesisState.Validate minus the one
// check that compares the local and counterparty client ids: they are
// identifiers on two different chains, and rejecting equality makes
// legitimately exported state unimportable.
func validateClientV2Genesis(gs clientv2types.GenesisState) error {
	seenIDs := make(map[string]struct{})
	for _, info := range gs.CounterpartyInfos {
		if len(info.ClientId) == 0 {
			return errors.New("invalid ibc clientv2 genesis: counterparty client id cannot be empty")
		}
		if len(info.CounterpartyInfo.MerklePrefix) == 0 {
			return errors.New("invalid ibc clientv2 genesis: counterparty merkle prefix cannot be empty")
		}
		if _, ok := seenIDs[info.ClientId]; ok {
			return fmt.Errorf("invalid ibc clientv2 genesis: duplicate counterparty client id %s", info.ClientId)
		}
		seenIDs[info.ClientId] = struct{}{}
	}
	return nil
}

// initClientV2Genesis is clientv2.InitGenesis with validateClientV2Genesis in
// place of ibc-go's Validate.
func initClientV2Genesis(ctx sdk.Context, k counterpartySetter, gs clientv2types.GenesisState) {
	if err := validateClientV2Genesis(gs); err != nil {
		panic(err)
	}
	for _, info := range gs.CounterpartyInfos {
		k.SetClientCounterparty(ctx, info.ClientId, info.CounterpartyInfo)
	}
}
