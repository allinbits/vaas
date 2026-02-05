package keeper

import (
	"errors"
	"fmt"

	providertypes "github.com/allinbits/vaas/x/vaas/provider/types"
	vaastypes "github.com/allinbits/vaas/x/vaas/types"

	abci "github.com/cometbft/cometbft/abci/types"

	clienttypes "github.com/cosmos/ibc-go/v10/modules/core/02-client/types"
	channeltypes "github.com/cosmos/ibc-go/v10/modules/core/04-channel/types"

	errorsmod "cosmossdk.io/errors"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

// OnAcknowledgementPacket handles acknowledgments for sent VSC packets.
// IBC v2 Note: This handler supports both channel-based (v1) and client-based (v2) lookups
// during the migration period.
func (k Keeper) OnAcknowledgementPacket(ctx sdk.Context, packet channeltypes.Packet, ack channeltypes.Acknowledgement) error {
	if err := ack.GetError(); err != "" {
		// The VSC packet data could not be successfully decoded.
		// This should never happen.
		k.Logger(ctx).Error(
			"recv ErrorAcknowledgement",
			"channelID", packet.SourceChannel,
			"error", err,
		)
		// IBC v1: Use channel-based lookup (deprecated, kept for backward compatibility)
		if consumerId, ok := k.GetChannelIdToConsumerId(ctx, packet.SourceChannel); ok {
			return k.StopAndPrepareForConsumerRemoval(ctx, consumerId)
		}
		return errorsmod.Wrapf(providertypes.ErrUnknownConsumerChannelId, "recv ErrorAcknowledgement on unknown channel %s", packet.SourceChannel)
	}
	return nil
}

// OnAcknowledgementPacketV2 handles acknowledgments for sent VSC packets using IBC v2 client-based routing.
//
// IBC v2 Note: In IBC v2, the source client ID is used to identify the consumer chain
// instead of the source channel ID. This handler should be used when packets are
// sent via SendVSCPacketsToChainV2.
func (k Keeper) OnAcknowledgementPacketV2(ctx sdk.Context, sourceClientID string, ackError string) error {
	if ackError != "" {
		k.Logger(ctx).Error(
			"recv ErrorAcknowledgement (v2)",
			"clientID", sourceClientID,
			"error", ackError,
		)
		// IBC v2: Use client-based lookup
		if consumerId, found := k.GetClientIdToConsumerId(ctx, sourceClientID); found {
			return k.StopAndPrepareForConsumerRemoval(ctx, consumerId)
		}
		return errorsmod.Wrapf(providertypes.ErrInvalidConsumerClient, "recv ErrorAcknowledgement on unknown client %s", sourceClientID)
	}
	return nil
}

// OnTimeoutPacket aborts the transaction if no chain exists for the destination channel,
// otherwise it stops the chain.
// IBC v2 Note: Timeout triggers immediate consumer removal, consistent with the spec.
func (k Keeper) OnTimeoutPacket(ctx sdk.Context, packet channeltypes.Packet) error {
	// IBC v1: Use channel-based lookup (deprecated, kept for backward compatibility)
	consumerId, found := k.GetChannelIdToConsumerId(ctx, packet.SourceChannel)
	if !found {
		k.Logger(ctx).Error("packet timeout, unknown channel:", "channelID", packet.SourceChannel)
		// abort transaction
		return errorsmod.Wrap(
			channeltypes.ErrInvalidChannelState,
			packet.SourceChannel,
		)
	}
	k.Logger(ctx).Info("packet timeout, deleting the consumer:", "consumerId", consumerId)
	return k.StopAndPrepareForConsumerRemoval(ctx, consumerId)
}

// OnTimeoutPacketV2 handles packet timeouts using IBC v2 client-based routing.
// A timeout triggers immediate consumer removal as per the IBC v2 spec.
//
// IBC v2 Note: In IBC v2, the source client ID is used to identify the consumer chain.
// Timeouts immediately trigger consumer removal since the consumer chain is considered
// unresponsive.
func (k Keeper) OnTimeoutPacketV2(ctx sdk.Context, sourceClientID string) error {
	// IBC v2: Use client-based lookup
	consumerId, found := k.GetClientIdToConsumerId(ctx, sourceClientID)
	if !found {
		k.Logger(ctx).Error("packet timeout (v2), unknown client:", "clientID", sourceClientID)
		return errorsmod.Wrapf(
			providertypes.ErrInvalidConsumerClient,
			"timeout on unknown client %s", sourceClientID,
		)
	}
	k.Logger(ctx).Info("packet timeout (v2), deleting the consumer:", "consumerId", consumerId, "clientId", sourceClientID)
	return k.StopAndPrepareForConsumerRemoval(ctx, consumerId)
}

// EndBlockVSU contains the EndBlock logic needed for
// the Validator Set Update sub-protocol
func (k Keeper) EndBlockVSU(ctx sdk.Context) ([]abci.ValidatorUpdate, error) {
	// logic to update the provider consensus validator set.
	valUpdates, err := k.ProviderValidatorUpdates(ctx)
	if err != nil {
		return []abci.ValidatorUpdate{}, fmt.Errorf("computing the provider consensus validator set: %w", err)
	}

	if k.BlocksUntilNextEpoch(ctx) == 0 {
		// only queue and send VSCPackets at the boundaries of an epoch

		// collect validator updates
		if err := k.QueueVSCPackets(ctx); err != nil {
			return []abci.ValidatorUpdate{}, fmt.Errorf("queueing consumer validator updates: %w", err)
		}

		// try sending VSC packets to all registered consumer chains;
		// if the CCV channel is not established for a consumer chain,
		// the updates will remain queued until the channel is established
		if err := k.SendVSCPackets(ctx); err != nil {
			return []abci.ValidatorUpdate{}, fmt.Errorf("sending consumer validator updates: %w", err)
		}
	}

	return valUpdates, nil
}

// ProviderValidatorUpdates returns changes in the provider consensus validator set
// from the last block to the current one.
// It retrieves the bonded validators from the staking module and creates a `ConsumerValidator` object for each validator.
// The maximum number of validators is determined by the `maxValidators` parameter.
// The function returns the difference between the current validator set and the next validator set as a list of `abci.ValidatorUpdate` objects.
func (k Keeper) ProviderValidatorUpdates(ctx sdk.Context) ([]abci.ValidatorUpdate, error) {
	// get the bonded validators from the staking module
	bondedValidators, err := k.stakingKeeper.GetBondedValidatorsByPower(ctx)
	if err != nil {
		return []abci.ValidatorUpdate{}, fmt.Errorf("getting bonded validators: %w", err)
	}

	// get the last validator set sent to consensus
	currentValidators, err := k.GetLastProviderConsensusValSet(ctx)
	if err != nil {
		return []abci.ValidatorUpdate{}, fmt.Errorf("getting last provider consensus validator set: %w", err)
	}

	nextValidators := []providertypes.ConsensusValidator{}
	maxValidators := k.GetMaxProviderConsensusValidators(ctx)
	// avoid out of range errors by bounding the max validators to the number of bonded validators
	if maxValidators > int64(len(bondedValidators)) {
		maxValidators = int64(len(bondedValidators))
	}
	for _, val := range bondedValidators[:maxValidators] {
		nextValidator, err := k.CreateProviderConsensusValidator(ctx, val)
		if err != nil {
			return []abci.ValidatorUpdate{},
				fmt.Errorf("creating provider consensus validator(%s): %w", val.OperatorAddress, err)
		}
		nextValidators = append(nextValidators, nextValidator)
	}

	// store the validator set we will send to consensus
	err = k.SetLastProviderConsensusValSet(ctx, nextValidators)
	if err != nil {
		return []abci.ValidatorUpdate{}, fmt.Errorf("setting the last provider consensus validator set: %w", err)
	}

	valUpdates := DiffValidators(currentValidators, nextValidators)

	return valUpdates, nil
}

// BlocksUntilNextEpoch returns the number of blocks until the next epoch starts
// Returns 0 if VSCPackets are sent in the current block,
// which is done in the first block of each epoch.
func (k Keeper) BlocksUntilNextEpoch(ctx sdk.Context) int64 {
	blocksSinceEpochStart := ctx.BlockHeight() % k.GetBlocksPerEpoch(ctx)

	if blocksSinceEpochStart == 0 {
		return 0
	} else {
		return k.GetBlocksPerEpoch(ctx) - blocksSinceEpochStart
	}
}

// SendVSCPackets iterates over all consumers chains with created IBC clients
// and sends pending VSC packets to the chains with established CCV channels.
// If the CCV channel is not established for a consumer chain,
// the updates will remain queued until the channel is established.
//
// IBC v2 Note: This function supports both channel-based (v1) and client-based (v2)
// routing. It first tries to send via channel (v1), and if no channel exists but
// a client is available, it will use client-based routing (v2).
//
// TODO (mpoke): iterate only over consumers with established channel -- GetAllChannelToConsumers
func (k Keeper) SendVSCPackets(ctx sdk.Context) error {
	for _, consumerId := range k.GetAllConsumersWithIBCClients(ctx) {
		if k.GetConsumerPhase(ctx, consumerId) != providertypes.CONSUMER_PHASE_LAUNCHED {
			// only send VSCPackets to launched chains
			continue
		}

		// IBC v1: Try channel-based routing first (for backward compatibility)
		if channelID, found := k.GetConsumerIdToChannelId(ctx, consumerId); found {
			if err := k.SendVSCPacketsToChain(ctx, consumerId, channelID); err != nil {
				return fmt.Errorf("sending VSCPacket to consumer, consumerId(%s): %w", consumerId, err)
			}
			continue
		}

		// IBC v2: Use client-based routing if channel doesn't exist
		if clientID, found := k.GetConsumerClientId(ctx, consumerId); found {
			if err := k.SendVSCPacketsToChainV2(ctx, consumerId, clientID); err != nil {
				return fmt.Errorf("sending VSCPacket (v2) to consumer, consumerId(%s): %w", consumerId, err)
			}
		}
	}
	return nil
}

// SendVSCPacketsToChain sends all queued VSC packets to the specified chain using IBC v1 channels.
//
// Deprecated: This function uses IBC v1 channel-based routing. Use SendVSCPacketsToChainV2
// for IBC v2 client-based routing.
func (k Keeper) SendVSCPacketsToChain(ctx sdk.Context, consumerId, channelId string) error {
	pendingPackets := k.GetPendingVSCPackets(ctx, consumerId)
	for _, data := range pendingPackets {
		// send packet over IBC
		err := vaastypes.SendIBCPacket(
			ctx,
			k.channelKeeper,
			channelId,                // source channel id
			vaastypes.ProviderPortID, // source port id
			data.GetBytes(),
			k.GetVAASTimeoutPeriod(ctx),
		)
		if err != nil {
			if errors.Is(err, clienttypes.ErrClientNotActive) {
				// IBC client is expired!
				// leave the packet data stored to be sent once the client is upgraded
				// the client cannot expire during iteration (in the middle of a block)
				k.Logger(ctx).Info("IBC client is expired, cannot send VSC, leaving packet data stored:",
					"consumerId", consumerId,
					"vscid", data.ValsetUpdateId,
				)
				return nil
			}
			// Not able to send packet over IBC!
			k.Logger(ctx).Error("cannot send VSC, removing consumer:", "consumerId", consumerId, "vscid", data.ValsetUpdateId, "err", err.Error())

			err := k.StopAndPrepareForConsumerRemoval(ctx, consumerId)
			if err != nil {
				k.Logger(ctx).Info("consumer chain failed to stop:", "consumerId", consumerId, "error", err.Error())
				// return fmt.Errorf("stopping consumer, consumerId(%s): %w", consumerId, err)
			}
			return nil
		}
	}
	k.DeletePendingVSCPackets(ctx, consumerId)

	return nil
}

// SendVSCPacketsToChainV2 sends all queued VSC packets to the specified chain using IBC v2 client-based routing.
//
// IBC v2 Note: This function uses client IDs for routing instead of channel IDs.
// Packets are sent directly to the consumer chain via the client, with the
// consumer application identifier (vaas/consumer) as the destination.
func (k Keeper) SendVSCPacketsToChainV2(ctx sdk.Context, consumerId, clientId string) error {
	// Check if IBCPacketHandler is available
	if k.ibcPacketHandler == nil {
		k.Logger(ctx).Debug("IBC v2 packet handler not configured, skipping v2 send",
			"consumerId", consumerId,
		)
		return nil
	}

	pendingPackets := k.GetPendingVSCPackets(ctx, consumerId)
	for _, data := range pendingPackets {
		// send packet over IBC v2
		sequence, err := vaastypes.SendIBCPacketV2(
			ctx,
			k.ibcPacketHandler,
			clientId,                // source client id (identifies destination chain)
			vaastypes.ConsumerAppID, // destination application identifier
			data.GetBytes(),
			k.GetVAASTimeoutPeriod(ctx),
		)
		if err != nil {
			if errors.Is(err, clienttypes.ErrClientNotActive) {
				// IBC client is expired!
				k.Logger(ctx).Info("IBC client is expired, cannot send VSC (v2), leaving packet data stored:",
					"consumerId", consumerId,
					"clientId", clientId,
					"vscid", data.ValsetUpdateId,
				)
				return nil
			}
			// Not able to send packet over IBC!
			k.Logger(ctx).Error("cannot send VSC (v2), removing consumer:",
				"consumerId", consumerId,
				"clientId", clientId,
				"vscid", data.ValsetUpdateId,
				"err", err.Error(),
			)

			err := k.StopAndPrepareForConsumerRemoval(ctx, consumerId)
			if err != nil {
				k.Logger(ctx).Info("consumer chain failed to stop:", "consumerId", consumerId, "error", err.Error())
			}
			return nil
		}

		k.Logger(ctx).Info("VSCPacket sent via IBC v2:",
			"consumerId", consumerId,
			"clientId", clientId,
			"vscid", data.ValsetUpdateId,
			"sequence", sequence,
		)
	}
	k.DeletePendingVSCPackets(ctx, consumerId)

	return nil
}

// QueueVSCPackets queues latest validator updates for every consumer chain
// with the IBC client created.
//
// TODO (mpoke): iterate only over consumers with established channel -- GetAllChannelToConsumers
func (k Keeper) QueueVSCPackets(ctx sdk.Context) error {
	valUpdateID := k.GetValidatorSetUpdateId(ctx) // current valset update ID

	// get the bonded validators from the staking module
	bondedValidators, err := k.GetLastBondedValidators(ctx)
	if err != nil {
		return fmt.Errorf("getting bonded validators: %w", err)
	}

	for _, consumerId := range k.GetAllConsumersWithIBCClients(ctx) {
		if k.GetConsumerPhase(ctx, consumerId) != providertypes.CONSUMER_PHASE_LAUNCHED {
			// only queue VSCPackets to launched chains
			continue
		}

		currentValSet, err := k.GetConsumerValSet(ctx, consumerId)
		if err != nil {
			return fmt.Errorf("getting consumer current validator set, consumerId(%s): %w", consumerId, err)
		}

		// compute consumer next validator set (all validators validate all consumers)
		valUpdates, err := k.ComputeConsumerNextValSet(ctx, bondedValidators, consumerId, currentValSet)
		if err != nil {
			return fmt.Errorf("computing consumer next validator set, consumerId(%s): %w", consumerId, err)
		}

		// check whether there are changes in the validator set
		if len(valUpdates) != 0 {
			// construct validator set change packet data
			packet := vaastypes.NewValidatorSetChangePacketData(valUpdates, valUpdateID)
			k.AppendPendingVSCPackets(ctx, consumerId, packet)
			k.Logger(ctx).Info("VSCPacket enqueued:",
				"consumerId", consumerId,
				"vscID", valUpdateID,
				"len updates", len(valUpdates),
			)
		}
	}

	k.IncrementValidatorSetUpdateId(ctx)

	return nil
}

// BeginBlockCIS contains the BeginBlock logic needed for the Consumer Initiated Slashing sub-protocol.
// Slash throttling has been removed.
func (k Keeper) BeginBlockCIS(ctx sdk.Context) {
	// Slash throttling removed - no-op
}

// EndBlockCIS contains the EndBlock logic needed for
// the Consumer Initiated Slashing sub-protocol
func (k Keeper) EndBlockCIS(ctx sdk.Context) {
	// set the ValsetUpdateBlockHeight
	blockHeight := uint64(ctx.BlockHeight()) + 1
	valUpdateID := k.GetValidatorSetUpdateId(ctx)
	k.SetValsetUpdateBlockHeight(ctx, valUpdateID, blockHeight)
	k.Logger(ctx).Debug("vscID was mapped to block height", "vscID", valUpdateID, "height", blockHeight)

	// prune previous consumer validator addresses that are no longer needed
	for _, consumerId := range k.GetAllConsumersWithIBCClients(ctx) {
		k.PruneKeyAssignments(ctx, consumerId)
	}
}
