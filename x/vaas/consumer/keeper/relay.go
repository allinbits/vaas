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
	// tracking an unexpected chain id, or a client other than the pinned
	// provider client, is rejected outright -- before the dedup check below
	// and every state mutation that follows it (SetLastVSCRecvTime, param
	// staging, valset apply). Anyone can permissionlessly create an IBC v2
	// client, so DestinationClient alone does not prove the packet came from
	// the provider; the chain-id gate and the client pin close that gap in
	// layers.
	if err := k.authenticateProviderChainID(ctx, consumerClientID); err != nil {
		return err
	}
	if err := k.enforcePinnedProviderClient(ctx, consumerClientID); err != nil {
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
// proves *some* chain is on the other end. The chain id is normally pinned at
// genesis from the provider-authored client state; as a fallback, the first
// VSC packet ever accepted teaches the consumer the provider's chain id from
// that packet's destination client. Every packet after that must arrive over
// a client tracking the same chain id, or it is rejected before any state
// changes (see the call site in OnRecvVSCPacketV2).
//
// Residual trust boundary: this only pins the chain-id *string*, so on its
// own it does not distinguish the real provider from a chain that reuses the
// same chain-id (a fork, or a chain deliberately renamed to collide). The
// client pin (see enforcePinnedProviderClient) closes most of that gap by
// refusing to follow inbound traffic to another client at all; what remains
// -- fraudulent history on the one pinned client -- is the job of the
// misbehaviour/light-client-fraud machinery, not this check.
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

// enforcePinnedProviderClient rejects a VSC packet unless it arrived over the
// consumer's pinned provider client, allowing the pin to move at most once in
// the chain's lifetime: from the unroutable client created at genesis to the
// first client that actually delivers a VSC packet.
//
// The pin's trust model: at NewChain genesis the consumer creates its own
// IBC client of the provider from client and consensus state the provider
// itself authored into the consumer genesis, and pins it (a restart restores
// the exported pin instead). That genesis client is a genuine light client of
// the real provider, but it can never carry packets: ibc-go only lets a
// client's recorded creator register the IBC v2 counterparty that packet
// routing requires, and a client created directly at genesis has no recorded
// creator. Whichever relayer serves the chain therefore creates its own,
// counterparty-linked client of the provider, and the first VSC packet
// delivered over such a client re-pins the consumer to it: ibc-go's
// RecvPacket has already proven the packet against that client's consensus
// state and registered counterparty, and authenticateProviderChainID has
// already checked it tracks the pinned provider chain id.
//
// From that moment the pin is permanent. A pinned client that has a
// registered counterparty is a routable client, so there is no legitimate
// reason for VSC traffic to ever arrive anywhere else: anyone can
// permissionlessly create a client of a look-alike chain reusing the
// provider's chain id and have packets routed over it, so following inbound
// traffic off the pin would let such a chain capture the consumer's validator
// set. If the pinned client dies (expires, is frozen), packet flow halts
// until governance revives it in place via ibc-go's MsgRecoverClient, which
// substitutes fresh client state under the SAME client id -- the pin survives
// recovery unchanged.
func (k Keeper) enforcePinnedProviderClient(ctx sdk.Context, consumerClientID string) error {
	pinned, found := k.GetProviderClientID(ctx)
	if !found {
		// Both genesis paths establish the pin (NewChain creates and pins the
		// genesis client; a restart restores the exported pin), so an absent
		// pin means a malformed genesis or corrupted state: fail closed.
		return errorsmod.Wrapf(types.ErrInvalidProviderClient,
			"no provider client pinned; rejecting VSC packet over client %s", consumerClientID)
	}
	if pinned == consumerClientID {
		return nil
	}
	if _, found := k.clientV2Keeper.GetClientCounterparty(ctx, pinned); found {
		return errorsmod.Wrapf(types.ErrInvalidProviderClient,
			"VSC packet arrived over client %s, but the provider client is pinned to %s",
			consumerClientID, pinned)
	}

	// The pinned client has no registered counterparty, so it is the genesis
	// client that packet routing can never reach: adopt the delivering client
	// as the permanent pin. Counterparties cannot be unregistered, so once a
	// routable client is pinned this branch is unreachable.
	k.SetProviderClientID(ctx, consumerClientID)
	k.Logger(ctx).Info("provider client pinned",
		"clientID", consumerClientID,
		"genesisClientID", pinned,
	)

	ctx.EventManager().EmitEvent(
		sdk.NewEvent(
			vaastypes.EventTypeChannelEstablished,
			sdk.NewAttribute(sdk.AttributeKeyModule, types.ModuleName),
			sdk.NewAttribute("client_id", consumerClientID),
		),
	)

	return nil
}
