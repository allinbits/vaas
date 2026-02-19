package provider

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/allinbits/vaas/x/vaas/provider/client/cli"
	"github.com/allinbits/vaas/x/vaas/provider/keeper"
	providertypes "github.com/allinbits/vaas/x/vaas/provider/types"
	"github.com/grpc-ecosystem/grpc-gateway/runtime"
	"github.com/spf13/cobra"

	abci "github.com/cometbft/cometbft/abci/types"

	"cosmossdk.io/core/appmodule"

	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/module"
	simtypes "github.com/cosmos/cosmos-sdk/types/simulation"
)

var (
	_ module.AppModule           = (*AppModule)(nil)
	_ module.AppModuleBasic      = (*AppModuleBasic)(nil)
	_ module.AppModuleSimulation = (*AppModule)(nil)
	_ module.HasName             = (*AppModule)(nil)
	_ module.HasConsensusVersion = (*AppModule)(nil)
	_ module.HasInvariants       = (*AppModule)(nil)
	_ module.HasServices         = (*AppModule)(nil)
	_ module.HasABCIGenesis      = (*AppModule)(nil)
	_ module.HasABCIEndBlock     = (*AppModule)(nil)
	_ appmodule.AppModule        = (*AppModule)(nil)
	_ appmodule.HasBeginBlocker  = (*AppModule)(nil)
)

// AppModuleBasic is the IBC Provider AppModuleBasic
type AppModuleBasic struct{}

// Name implements AppModuleBasic interface
func (AppModuleBasic) Name() string {
	return providertypes.ModuleName
}

// IsOnePerModuleType implements the depinject.OnePerModuleType interface.
func (am AppModule) IsOnePerModuleType() {}

// IsAppModule implements the appmodule.AppModule interface.
func (am AppModule) IsAppModule() {}

// RegisterLegacyAminoCodec implements AppModuleBasic interface
func (AppModuleBasic) RegisterLegacyAminoCodec(cdc *codec.LegacyAmino) {
	providertypes.RegisterLegacyAminoCodec(cdc)
}

// RegisterInterfaces registers module concrete types into protobuf Any.
func (AppModuleBasic) RegisterInterfaces(registry codectypes.InterfaceRegistry) {
	providertypes.RegisterInterfaces(registry)
}

// DefaultGenesis returns default genesis state as raw bytes for the ibc
// provider module.
func (AppModuleBasic) DefaultGenesis(cdc codec.JSONCodec) json.RawMessage {
	return cdc.MustMarshalJSON(providertypes.DefaultGenesisState())
}

// ValidateGenesis performs genesis state validation for the ibc provider module.
func (AppModuleBasic) ValidateGenesis(cdc codec.JSONCodec, config client.TxEncodingConfig, bz json.RawMessage) error {
	var data providertypes.GenesisState
	if err := cdc.UnmarshalJSON(bz, &data); err != nil {
		return fmt.Errorf("failed to unmarshal %s genesis state: %w", providertypes.ModuleName, err)
	}

	return data.Validate()
}

// RegisterGRPCGatewayRoutes registers the gRPC Gateway routes for the ibc-provider module.
func (AppModuleBasic) RegisterGRPCGatewayRoutes(clientCtx client.Context, mux *runtime.ServeMux) {
	err := providertypes.RegisterQueryHandlerClient(context.Background(), mux, providertypes.NewQueryClient(clientCtx))
	if err != nil {
		// same behavior as in cosmos-sdk
		panic(err)
	}
}

// GetTxCmd implements AppModuleBasic interface
func (AppModuleBasic) GetTxCmd() *cobra.Command {
	return cli.GetTxCmd()
}

// GetQueryCmd implements AppModuleBasic interface
func (AppModuleBasic) GetQueryCmd() *cobra.Command {
	return cli.NewQueryCmd()
}

// AppModule represents the AppModule for this module
type AppModule struct {
	AppModuleBasic
	keeper *keeper.Keeper
}

// NewAppModule creates a new provider module
func NewAppModule(k *keeper.Keeper) AppModule {
	return AppModule{
		keeper: k,
	}
}

// RegisterInvariants implements the AppModule interface
func (am AppModule) RegisterInvariants(ir sdk.InvariantRegistry) {
	keeper.RegisterInvariants(ir, am.keeper)
}

// RegisterServices registers module services.
func (am AppModule) RegisterServices(cfg module.Configurator) {
	providertypes.RegisterMsgServer(cfg.MsgServer(), keeper.NewMsgServerImpl(am.keeper))
	providertypes.RegisterQueryServer(cfg.QueryServer(), am.keeper)
	// Note: Migrations are not registered as this is a fresh module start
}

// InitGenesis performs genesis initialization for the provider module. It returns validator updates
// by selecting the first MaxProviderConsensusValidators from the staking module's validator set.
// Note: This method along with ValidateGenesis satisfies the CCV spec:
// https://github.com/cosmos/ibc/blob/main/spec/app/ics-028-cross-chain-validation/methods.md#ccv-pcf-initg1
func (am AppModule) InitGenesis(ctx sdk.Context, cdc codec.JSONCodec, data json.RawMessage) []abci.ValidatorUpdate {
	var genesisState providertypes.GenesisState
	cdc.MustUnmarshalJSON(data, &genesisState)

	return am.keeper.InitGenesis(ctx, &genesisState)
}

// ExportGenesis returns the exported genesis state as raw bytes for the provider
// module.
func (am AppModule) ExportGenesis(ctx sdk.Context, cdc codec.JSONCodec) json.RawMessage {
	gs := am.keeper.ExportGenesis(ctx)
	return cdc.MustMarshalJSON(gs)
}

// ConsensusVersion implements AppModule/ConsensusVersion.
func (AppModule) ConsensusVersion() uint64 { return 1 }

// BeginBlock implements the AppModule interface
func (am AppModule) BeginBlock(ctx context.Context) error {
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	// Create clients to consumer chains that are due to be spawned
	if err := am.keeper.BeginBlockLaunchConsumers(sdkCtx); err != nil {
		return err
	}
	// Stop and remove state for any consumer chains that are due to be stopped
	if err := am.keeper.BeginBlockRemoveConsumers(sdkCtx); err != nil {
		return err
	}

	// Collect fees from consumer chains. Failures here should not halt block processing.
	collectedFees, err := am.keeper.CollectFeesFromConsumers(sdkCtx)
	if err != nil {
		sdkCtx.Logger().Error("failed to collect fees from consumers", "err", err)
	} else {
		// Distribute collected fees to validators. Failures here should also not halt block processing.
		if err := am.keeper.DistributeFeesToValidators(sdkCtx, collectedFees); err != nil {
			sdkCtx.Logger().Error("failed to distribute collected fees to validators", "err", err)
		}
	}
	return nil
}

// EndBlock implements the AppModule interface
func (am AppModule) EndBlock(ctx context.Context) ([]abci.ValidatorUpdate, error) {
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	// EndBlock logic needed for the Validator Set Update sub-protocol
	return am.keeper.EndBlockVSU(sdkCtx)
}

// AppModuleSimulation functions

// GenerateGenesisState creates a randomized GenState of the transfer module.
func (AppModule) GenerateGenesisState(simState *module.SimulationState) {
	// Simulation not implemented for this module
}

// RegisterStoreDecoder registers a decoder for provider module's types
func (am AppModule) RegisterStoreDecoder(sdr simtypes.StoreDecoderRegistry) {
}

// WeightedOperations returns the all the provider module operations with their respective weights.
func (am AppModule) WeightedOperations(_ module.SimulationState) []simtypes.WeightedOperation {
	return nil
}
