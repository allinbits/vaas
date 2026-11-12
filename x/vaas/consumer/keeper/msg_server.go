package keeper

import (
	"bytes"
	"context"
	"fmt"

	abci "github.com/cometbft/cometbft/abci/types"

	ibcexported "github.com/cosmos/ibc-go/v10/modules/core/exported"
	ibctmtypes "github.com/cosmos/ibc-go/v10/modules/light-clients/07-tendermint"

	errorsmod "cosmossdk.io/errors"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/bech32"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"

	"github.com/allinbits/vaas/x/vaas/consumer/types"
	vaastypes "github.com/allinbits/vaas/x/vaas/types"
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
// params -- or by the chain's authority. What is validated is coherence, not provenance:
// the client must exist, be a tendermint client tracking the provider chain
// id pinned at genesis, be Active, and have a registered IBC v2 counterparty.
//
// The pin is permanent. Client ids are identity here -- the photon fee denom
// derives from the pinned client, and the provider keys downtime state by its
// own client ids -- so a pin that died is recovered in place by the chain's
// authority via ibc-go's MsgRecoverClient, which substitutes fresh client state under the
// same client id. Authorization compares decoded address bytes rather than
// bech32 strings: the provider seeds the owner rendered under its own prefix,
// while the signer arrives rendered under the consumer's.
func (k msgServer) SetProviderClient(goCtx context.Context, msg *types.MsgSetProviderClient) (*types.MsgSetProviderClientResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	if !k.isProviderClientAuthority(ctx, msg.Signer) {
		return nil, errorsmod.Wrapf(types.ErrInvalidProviderClient,
			"signer %s is neither the consumer owner nor the chain's authority", msg.Signer)
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
			vaastypes.EventTypeClientEstablished,
			sdk.NewAttribute(sdk.AttributeKeyModule, types.ModuleName),
			sdk.NewAttribute("client_id", msg.ClientId),
		),
	)

	return &types.MsgSetProviderClientResponse{}, nil
}

// isProviderClientAuthority reports whether signer may pin the provider
// client: the chain's authority, or the owner the provider seeded into the
// consumer params. Owner comparison decodes both sides to raw address bytes,
// since the provider renders the owner under its own bech32 prefix while the
// signer is rendered under the consumer's; the same key must match under
// either.
func (k msgServer) isProviderClientAuthority(ctx sdk.Context, signer string) bool {
	return signer == k.GetAuthority() || k.isConsumerOwner(ctx, signer)
}

// isConsumerOwner reports whether signer is the owner the provider seeded
// into the consumer params, comparing raw address bytes: the provider renders
// the owner under its own bech32 prefix, the signer arrives under the
// consumer's, and the same key must match under either. No seeded owner
// means no one is.
func (k msgServer) isConsumerOwner(ctx sdk.Context, signer string) bool {
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

// OwnerExec executes the messages the consumer's owner submits, in order, as
// the consumer module account (types.OwnerExecAuthority), through the app's
// message router. It is the authority of a consumer that has no governance
// of its own: whatever keepers the embedding app names the consumer module
// the authority of (the reference app: IBC, upgrades, consensus and every
// module's parameters, the consumer's included), the owner may act on, and
// nothing else, since every carried message must be signed by that account
// alone and no other module account can be impersonated. The owner is the
// one the provider seeded into the consumer params, matched on raw address
// bytes like the pin's; the message cannot nest.
//
// Execution is all or nothing: the transaction fails on the first message
// that is refused or fails, and the events of the executed messages are
// emitted with the index of the message they came from.
func (k msgServer) OwnerExec(goCtx context.Context, msg *types.MsgOwnerExec) (*types.MsgOwnerExecResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	if !k.isConsumerOwner(ctx, msg.Signer) {
		return nil, errorsmod.Wrapf(types.ErrInvalidOwnerExec, "signer %s is not the consumer owner", msg.Signer)
	}
	if k.router == nil {
		return nil, errorsmod.Wrap(types.ErrInvalidOwnerExec, "the app wires no message router for the consumer module")
	}
	msgs, err := msg.GetMsgs()
	if err != nil {
		return nil, errorsmod.Wrap(types.ErrInvalidOwnerExec, err.Error())
	}
	if len(msgs) == 0 {
		return nil, errorsmod.Wrap(types.ErrInvalidOwnerExec, "no messages to execute")
	}

	authority := types.OwnerExecAuthority()
	for i, inner := range msgs {
		typeURL := sdk.MsgTypeURL(inner)
		if _, nested := inner.(*types.MsgOwnerExec); nested {
			return nil, errorsmod.Wrapf(types.ErrInvalidOwnerExec, "message %d nests another owner exec", i)
		}
		signers, _, err := k.cdc.GetMsgV1Signers(inner)
		if err != nil {
			return nil, errorsmod.Wrapf(types.ErrInvalidOwnerExec, "message %d (%s): reading its signers: %s", i, typeURL, err)
		}
		if len(signers) != 1 || !bytes.Equal(signers[0], authority) {
			return nil, errorsmod.Wrapf(types.ErrInvalidOwnerExec,
				"message %d (%s) must be signed by the consumer module account %s alone", i, typeURL, authority.String())
		}
		if v, ok := inner.(sdk.HasValidateBasic); ok {
			if err := v.ValidateBasic(); err != nil {
				return nil, errorsmod.Wrapf(types.ErrInvalidOwnerExec, "message %d (%s): %s", i, typeURL, err)
			}
		}
		handler := k.router.Handler(inner)
		if handler == nil {
			return nil, errorsmod.Wrapf(sdkerrors.ErrUnknownRequest, "message %d: unrecognized message route %s", i, typeURL)
		}
		res, err := handler(ctx, inner)
		if err != nil {
			return nil, errorsmod.Wrapf(err, "executing message %d (%s)", i, typeURL)
		}
		ctx.EventManager().EmitEvents(ownerExecEvents(res.Events, i))
	}
	if err := k.providerClientCoherent(ctx); err != nil {
		return nil, err
	}
	if err := k.ownerUsable(ctx); err != nil {
		return nil, err
	}

	k.Logger(ctx).Info("owner exec", "signer", msg.Signer, "messages", len(msgs))
	ctx.EventManager().EmitEvent(sdk.NewEvent(
		vaastypes.EventTypeOwnerExec,
		sdk.NewAttribute(sdk.AttributeKeyModule, types.ModuleName),
		sdk.NewAttribute(vaastypes.AttributeOwner, msg.Signer),
		sdk.NewAttribute(vaastypes.AttributeNumMessages, fmt.Sprintf("%d", len(msgs))),
	))
	return &types.MsgOwnerExecResponse{}, nil
}

// providerClientCoherent reports, as an error, that the pinned provider
// client tracks a chain id other than the provider's: the one state an owner
// exec may not leave behind. A recovery (MsgRecoverClient) substitutes a
// client of any chain id, and the pin's coherence rule (SetProviderClient)
// holds for its recovery too; a pin left tracking another chain would accept
// no packet (relay.go, authenticateProviderChainID) and could never be moved
// back. Nothing is checked while no client is pinned, or while the pinned
// client has no state.
func (k Keeper) providerClientCoherent(ctx sdk.Context) error {
	clientID, found := k.GetProviderClientID(ctx)
	if !found || clientID == "" {
		return nil
	}
	providerChainID, found := k.GetProviderChainId(ctx)
	if !found {
		return nil
	}
	clientState, found := k.clientKeeper.GetClientState(ctx, clientID)
	if !found {
		return nil
	}
	tmClientState, ok := clientState.(*ibctmtypes.ClientState)
	if !ok {
		return errorsmod.Wrapf(types.ErrInvalidOwnerExec, "the pinned provider client %s is no tendermint client any more", clientID)
	}
	if tmClientState.ChainId != providerChainID {
		return errorsmod.Wrapf(types.ErrInvalidOwnerExec,
			"the pinned provider client %s tracks chain id %s, not the provider chain id %s: a recovery must substitute a client of the provider chain",
			clientID, tmClientState.ChainId, providerChainID)
	}
	return nil
}

// ownerUsable reports, as an error, an owner_address an owner exec may not
// leave behind: empty, not a bech32 address, or the consumer module account
// itself. MsgUpdateParams replaces every consumer param but the two
// provider-owned ones, so an exec carrying it hands the chain to whatever
// owner_address it names; on a consumer whose authority is the consumer
// module account, an owner that is no account anyone holds would leave the
// chain with no authority for good, and nothing on the chain could restore
// one.
func (k Keeper) ownerUsable(ctx sdk.Context) error {
	owner := k.GetConsumerParams(ctx).OwnerAddress
	if owner == "" {
		return errorsmod.Wrap(types.ErrInvalidOwnerExec,
			"the exec leaves the consumer with no owner: a MsgUpdateParams carries the full params, owner_address included")
	}
	_, ownerBytes, err := bech32.DecodeAndConvert(owner)
	if err != nil {
		return errorsmod.Wrapf(types.ErrInvalidOwnerExec, "the exec leaves an owner_address that is not a bech32 address: %s", err)
	}
	if bytes.Equal(ownerBytes, types.OwnerExecAuthority()) {
		return errorsmod.Wrap(types.ErrInvalidOwnerExec,
			"the exec makes the consumer module account its own owner, which no one can sign for")
	}
	return nil
}

// ownerExecEvents returns the events an executed message produced, each
// tagged with the index of the message in the owner exec it came from: the
// router hands them back in the result rather than emitting them on the
// context.
func ownerExecEvents(events []abci.Event, index int) sdk.Events {
	out := make(sdk.Events, 0, len(events))
	for _, event := range events {
		event.Attributes = append(event.Attributes, abci.EventAttribute{Key: vaastypes.AttributeOwnerExecMsgIndex, Value: fmt.Sprintf("%d", index)})
		out = append(out, sdk.Event(event))
	}
	return out
}
