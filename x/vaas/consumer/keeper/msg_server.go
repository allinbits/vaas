package keeper

import (
	"bytes"
	"context"

	"github.com/allinbits/vaas/x/vaas/consumer/types"
	vaastypes "github.com/allinbits/vaas/x/vaas/types"

	ibcexported "github.com/cosmos/ibc-go/v10/modules/core/exported"
	ibctmtypes "github.com/cosmos/ibc-go/v10/modules/light-clients/07-tendermint"

	errorsmod "cosmossdk.io/errors"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/bech32"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"
)

type msgServer struct {
	*Keeper
}

// NewMsgServerImpl returns an implementation of the bank MsgServer interface
// for the provided Keeper.
func NewMsgServerImpl(keeper *Keeper) types.MsgServer {
	return &msgServer{Keeper: keeper}
}

var _ types.MsgServer = msgServer{}

// UpdateParams updates the params.
func (k msgServer) UpdateParams(goCtx context.Context, msg *types.MsgUpdateParams) (*types.MsgUpdateParamsResponse, error) {
	if k.GetAuthority() != msg.Authority {
		return nil, errorsmod.Wrapf(govtypes.ErrInvalidSigner, "invalid authority; expected %s, got %s", k.authority, msg.Authority)
	}

	if err := msg.Params.Validate(); err != nil {
		return nil, err
	}

	ctx := sdk.UnwrapSDKContext(goCtx)

	// SignedBlocksWindow/MinSignedPerWindow are provider-owned: distributed
	// via consumer genesis and VSC packets, not locally updatable through
	// this message. Preserve the stored values over whatever the message
	// carries.
	current := k.Keeper.GetConsumerParams(ctx)
	params := msg.Params
	params.SignedBlocksWindow = current.SignedBlocksWindow
	params.MinSignedPerWindow = current.MinSignedPerWindow
	k.Keeper.SetParams(ctx, params)

	return &types.MsgUpdateParamsResponse{}, nil
}

// SetProviderClient pins the IBC client the consumer treats as the provider,
// exactly once, at bootstrap.
//
// Nothing here is discovered or inferred: with no client created at genesis
// (a keeper-created client has no recorded creator, so its IBC v2
// counterparty could never be registered and no packet could ever reach it),
// the chain starts with no pin, every VSC packet is rejected, and the pin is
// an explicit statement by the owner the provider seeded into the consumer
// params -- or by governance. What is validated is coherence, not provenance:
// the client must exist, be a tendermint client tracking the provider chain
// id pinned at genesis, be Active, and have a registered IBC v2 counterparty.
//
// The pin is permanent. Client ids are identity here -- the photon fee denom
// derives from the pinned client, and the provider keys downtime state by its
// own client ids -- so a pin that died is recovered in place by governance via
// ibc-go's MsgRecoverClient, which substitutes fresh client state under the
// same client id. Authorization compares decoded address bytes rather than
// bech32 strings: the provider seeds the owner rendered under its own prefix,
// while the signer arrives rendered under the consumer's.
func (k msgServer) SetProviderClient(goCtx context.Context, msg *types.MsgSetProviderClient) (*types.MsgSetProviderClientResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	if !k.isProviderClientAuthority(ctx, msg.Signer) {
		return nil, errorsmod.Wrapf(types.ErrInvalidProviderClient,
			"signer %s is neither the consumer owner nor the governance authority", msg.Signer)
	}

	if pinned, found := k.GetProviderClientID(ctx); found && pinned != "" {
		return nil, errorsmod.Wrapf(types.ErrInvalidProviderClient,
			"provider client already pinned to %s; the pin is permanent (recover a dead client in place via MsgRecoverClient)", pinned)
	}

	providerChainID, found := k.GetProviderChainId(ctx)
	if !found {
		return nil, errorsmod.Wrap(types.ErrInvalidProviderClient,
			"no provider chain id pinned at genesis; cannot validate a provider client against it")
	}

	clientState, found := k.clientKeeper.GetClientState(ctx, msg.ClientId)
	if !found {
		return nil, errorsmod.Wrapf(types.ErrInvalidProviderClient, "client %s does not exist", msg.ClientId)
	}
	tmClientState, ok := clientState.(*ibctmtypes.ClientState)
	if !ok {
		return nil, errorsmod.Wrapf(types.ErrInvalidProviderClient, "client %s is not a tendermint client", msg.ClientId)
	}
	if tmClientState.ChainId != providerChainID {
		return nil, errorsmod.Wrapf(types.ErrInvalidProviderClient,
			"client %s tracks chain id %s, expected the provider chain id %s pinned at genesis",
			msg.ClientId, tmClientState.ChainId, providerChainID)
	}
	if status := k.clientKeeper.GetClientStatus(ctx, msg.ClientId); status != ibcexported.Active {
		return nil, errorsmod.Wrapf(types.ErrInvalidProviderClient,
			"client %s is %s, not %s", msg.ClientId, status, ibcexported.Active)
	}
	if cp, found := k.clientV2Keeper.GetClientCounterparty(ctx, msg.ClientId); !found || cp.ClientId == "" {
		return nil, errorsmod.Wrapf(types.ErrInvalidProviderClient,
			"client %s has no registered IBC v2 counterparty, so no packet can be routed over it", msg.ClientId)
	}

	k.SetProviderClientID(ctx, msg.ClientId)
	k.Logger(ctx).Info("provider client pinned", "clientID", msg.ClientId, "signer", msg.Signer)
	ctx.EventManager().EmitEvent(
		sdk.NewEvent(
			vaastypes.EventTypeChannelEstablished,
			sdk.NewAttribute(sdk.AttributeKeyModule, types.ModuleName),
			sdk.NewAttribute("client_id", msg.ClientId),
		),
	)

	return &types.MsgSetProviderClientResponse{}, nil
}

// isProviderClientAuthority reports whether signer may pin the provider
// client: the governance authority, or the owner the provider seeded into the
// consumer params. Owner comparison decodes both sides to raw address bytes,
// since the provider renders the owner under its own bech32 prefix while the
// signer is rendered under the consumer's; the same key must match under
// either.
func (k msgServer) isProviderClientAuthority(ctx sdk.Context, signer string) bool {
	if signer == k.GetAuthority() {
		return true
	}
	owner := k.GetConsumerParams(ctx).OwnerAddress
	if owner == "" {
		return false
	}
	_, ownerBytes, err := bech32.DecodeAndConvert(owner)
	if err != nil {
		return false
	}
	_, signerBytes, err := bech32.DecodeAndConvert(signer)
	if err != nil {
		return false
	}
	return bytes.Equal(ownerBytes, signerBytes)
}
