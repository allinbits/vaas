package keeper

import (
	"strconv"

	"github.com/allinbits/vaas/x/vaas/consumer/types"
	vaastypes "github.com/allinbits/vaas/x/vaas/types"

	abci "github.com/cometbft/cometbft/abci/types"

	ibctmtypes "github.com/cosmos/ibc-go/v10/modules/light-clients/07-tendermint"

	errorsmod "cosmossdk.io/errors"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

// OnRecvVSCPacketV2 handles a validator-set-change packet from the provider.
// consumerClientID is the consumer's own IBC v2 client that received this
// packet (i.e. packet.DestinationClient, guaranteed by ibc-go's RecvPacket to
// have a registered counterparty before our callback ever runs) -- the value
// SendEvidencePackets later needs to address packets back to the provider.
func (k Keeper) OnRecvVSCPacketV2(ctx sdk.Context, consumerClientID string, newChanges vaastypes.ValidatorSetChangePacketData) error {
	if err := newChanges.Validate(); err != nil {
		return errorsmod.Wrapf(err, "error validating VSCPacket data")
	}

	// Authenticate the packet's source before touching any state: a client
	// tracking an unexpected chain id is rejected outright, before the
	// dedup check below and every state mutation that follows it
	// (SetLastVSCRecvTime, the ProviderClientID heal, param staging, valset
	// apply). Anyone can permissionlessly create an IBC v2 client, so
	// DestinationClient alone does not prove the packet came from the
	// provider; pinning the chain id closes that gap.
	if err := k.authenticateProviderChainID(ctx, consumerClientID); err != nil {
		return err
	}

	highestID, found, err := k.GetHighestValsetUpdateID(ctx)
	if err != nil {
		return errorsmod.Wrapf(err, "error getting highest valset update ID")
	}

	if found && newChanges.ValsetUpdateId <= highestID {
		k.Logger(ctx).Info("skipping out-of-order VSCPacket",
			"packetVscID", newChanges.ValsetUpdateId,
			"highestVscID", highestID,
			"consumerClientID", consumerClientID,
		)
		return nil
	}

	k.SetLastVSCRecvTime(ctx, ctx.BlockTime())

	if newChanges.DowntimeParams != nil {
		k.StageDowntimeParams(ctx, *newChanges.DowntimeParams)
	}

	// The stored client follows whichever client is actually delivering VSC
	// packets: the first value ever seen is not authoritative, because at
	// genesis it is a placeholder client the consumer creates for itself
	// before the relayer establishes the real, counterparty-linked client.
	if current, found := k.GetProviderClientID(ctx); !found || current != consumerClientID {
		k.SetProviderClientID(ctx, consumerClientID)
		k.Logger(ctx).Info("Provider client established", "clientID", consumerClientID)

		ctx.EventManager().EmitEvent(
			sdk.NewEvent(
				vaastypes.EventTypeChannelEstablished,
				sdk.NewAttribute(sdk.AttributeKeyModule, types.ModuleName),
				sdk.NewAttribute("client_id", consumerClientID),
			),
		)
	}

	k.SetConsumerInDebt(ctx, newChanges.ConsumerInDebt)

	// Set pending changes: snapshot packets replace the set; diff packets accumulate.
	var pendingChanges []abci.ValidatorUpdate
	if newChanges.IsSnapshot {
		pendingChanges = k.computeReplaceUpdates(ctx, newChanges.ValidatorUpdates)
		// Surface snapshot resyncs (not ordinary diffs) so operators -- and the
		// e2e -- can observe that a behind consumer was healed by a full-set
		// replacement rather than an accumulated diff. Emitted both as an event
		// (structured/queryable) and a log line (the e2e asserts on the log).
		ctx.EventManager().EmitEvent(
			sdk.NewEvent(
				vaastypes.EventTypeSnapshotResync,
				sdk.NewAttribute(sdk.AttributeKeyModule, types.ModuleName),
				sdk.NewAttribute(vaastypes.AttributeValSetUpdateID, strconv.FormatUint(newChanges.ValsetUpdateId, 10)),
				sdk.NewAttribute(vaastypes.AttributeNumValidators, strconv.Itoa(len(newChanges.ValidatorUpdates))),
			),
		)
		k.Logger(ctx).Info("applied snapshot resync",
			"vscID", newChanges.ValsetUpdateId,
			"numValidators", len(newChanges.ValidatorUpdates),
		)
	} else {
		currentValUpdates := []abci.ValidatorUpdate{}
		if currentChanges, exists := k.GetPendingChanges(ctx); exists {
			currentValUpdates = currentChanges.ValidatorUpdates
		}
		pendingChanges = vaastypes.AccumulateChanges(currentValUpdates, newChanges.ValidatorUpdates)
	}

	k.SetPendingChanges(ctx, vaastypes.ValidatorSetChangePacketData{
		ValidatorUpdates: pendingChanges,
	})

	blockHeight := uint64(ctx.BlockHeight()) + 1
	k.SetHeightValsetUpdateID(ctx, blockHeight, newChanges.ValsetUpdateId)
	k.Logger(ctx).Debug("block height was mapped to vscID", "height", blockHeight, "vscID", newChanges.ValsetUpdateId)

	if err := k.SetHighestValsetUpdateID(ctx, newChanges.ValsetUpdateId); err != nil {
		return errorsmod.Wrapf(err, "error setting highest valset update ID")
	}

	k.Logger(ctx).Info("finished receiving/handling VSCPacket",
		"vscID", newChanges.ValsetUpdateId,
		"len updates", len(newChanges.ValidatorUpdates),
		"consumerClientID", consumerClientID,
	)
	return nil
}

// authenticateProviderChainID pins the consumer's inbound VSC traffic to a
// single provider chain id. Anyone can permissionlessly create an IBC v2
// client and get a relayer to route packets through it, so the fact that
// consumerClientID is a registered, counterparty-linked client is not by
// itself proof the packets originate from the real provider chain -- it only
// proves *some* chain is on the other end. The first VSC packet ever
// accepted teaches the consumer the provider's chain id from that packet's
// destination client; every packet after that must arrive over a client
// tracking the same chain id, or it is rejected before any state changes
// (see the call site in OnRecvVSCPacketV2). A same-chain-id client
// replacement -- e.g. swapping an expired/frozen client for a fresh one --
// remains allowed, since the ProviderClientID heal logic in OnRecvVSCPacketV2
// is unaffected by this gate as long as the chain id matches.
//
// Residual trust boundary: this only pins the chain-id *string*. A chain
// that reuses the same chain-id (a fork, or a chain deliberately renamed to
// collide) still has to produce a light-client history that convinces the
// consumer's tendermint light client of that chain id; forging that history
// is the job of the misbehaviour/light-client-fraud machinery, not this
// check.
func (k Keeper) authenticateProviderChainID(ctx sdk.Context, consumerClientID string) error {
	clientState, found := k.clientKeeper.GetClientState(ctx, consumerClientID)
	if !found {
		return errorsmod.Wrapf(types.ErrInvalidProviderClient, "no client state found for client %s", consumerClientID)
	}
	tmClientState, ok := clientState.(*ibctmtypes.ClientState)
	if !ok {
		return errorsmod.Wrapf(types.ErrInvalidProviderClient, "client %s is not a tendermint client", consumerClientID)
	}

	pinned, found := k.GetProviderChainId(ctx)
	if !found {
		k.SetProviderChainId(ctx, tmClientState.ChainId)
		return nil
	}

	if pinned != tmClientState.ChainId {
		return errorsmod.Wrapf(types.ErrInvalidProviderClient,
			"client %s tracks chain id %s, expected pinned provider chain id %s",
			consumerClientID, tmClientState.ChainId, pinned)
	}

	return nil
}
