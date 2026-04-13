package keeper

import (
	"errors"
	"fmt"

	providertypes "github.com/allinbits/vaas/x/vaas/provider/types"
	vaastypes "github.com/allinbits/vaas/x/vaas/types"

	abci "github.com/cometbft/cometbft/abci/types"

	clienttypes "github.com/cosmos/ibc-go/v10/modules/core/02-client/types"
	channeltypesv2 "github.com/cosmos/ibc-go/v10/modules/core/04-channel/v2/types"
	ibcexported "github.com/cosmos/ibc-go/v10/modules/core/exported"
	ibctmtypes "github.com/cosmos/ibc-go/v10/modules/light-clients/07-tendermint"

	errorsmod "cosmossdk.io/errors"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

func (k Keeper) OnAcknowledgementPacketV2(ctx sdk.Context, sourceClientID string, ackError string) error {
	if ackError != "" {
		k.Logger(ctx).Error(
			"recv ErrorAcknowledgement",
			"clientID", sourceClientID,
			"error", ackError,
		)
		if consumerId, found := k.GetClientIdToConsumerId(ctx, sourceClientID); found {
			return k.StopAndPrepareForConsumerRemoval(ctx, consumerId)
		}
		return errorsmod.Wrapf(providertypes.ErrInvalidConsumerClient, "recv ErrorAcknowledgement on unknown client %s", sourceClientID)
	}
	return nil
}

func (k Keeper) OnTimeoutPacketV2(ctx sdk.Context, sourceClientID string) error {
	consumerId, found := k.GetClientIdToConsumerId(ctx, sourceClientID)
	if !found {
		k.Logger(ctx).Error("packet timeout, unknown client:", "clientID", sourceClientID)
		return errorsmod.Wrapf(
			providertypes.ErrInvalidConsumerClient,
			"timeout on unknown client %s", sourceClientID,
		)
	}
	k.Logger(ctx).Info("packet timeout, deleting the consumer:", "consumerId", consumerId, "clientId", sourceClientID)
	return k.StopAndPrepareForConsumerRemoval(ctx, consumerId)
}

// EndBlockVSU contains the EndBlock logic needed for
// the Validator Set Update sub-protocol
func (k Keeper) EndBlockVSU(ctx sdk.Context) ([]abci.ValidatorUpdate, error) {
	valUpdates, err := k.ProviderValidatorUpdates(ctx)
	if err != nil {
		return []abci.ValidatorUpdate{}, fmt.Errorf("computing the provider consensus validator set: %w", err)
	}

	k.DiscoverConsumerClients(ctx)

	if k.BlocksUntilNextEpoch(ctx) == 0 {
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

func (k Keeper) DiscoverConsumerClients(ctx sdk.Context) {
	for _, consumerId := range k.GetAllLaunchedConsumersWithoutClient(ctx) {
		chainId, err := k.GetConsumerChainId(ctx, consumerId)
		if err != nil {
			continue
		}

		var foundClientID string
		k.clientKeeper.IterateClientStates(ctx, nil, func(clientID string, cs ibcexported.ClientState) bool {
			tmCs, ok := cs.(*ibctmtypes.ClientState)
			if !ok {
				return false
			}
			if tmCs.ChainId == chainId {
				foundClientID = clientID
				return true
			}
			return false
		})

		if foundClientID != "" {
			k.SetConsumerClientId(ctx, consumerId, foundClientID)
			k.Logger(ctx).Info("discovered relayer client for consumer",
				"consumerId", consumerId,
				"clientId", foundClientID,
				"chainId", chainId,
			)
		}
	}
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

func (k Keeper) SendVSCPackets(ctx sdk.Context) error {
	for _, consumerId := range k.GetAllConsumersWithIBCClients(ctx) {
		if k.GetConsumerPhase(ctx, consumerId) != providertypes.CONSUMER_PHASE_LAUNCHED {
			continue
		}

		clientID, found := k.GetConsumerClientId(ctx, consumerId)
		if !found {
			continue
		}

		if err := k.SendVSCPacketsToChain(ctx, consumerId, clientID); err != nil {
			return fmt.Errorf("sending VSCPacket to consumer, consumerId(%s): %w", consumerId, err)
		}
	}
	return nil
}

func (k Keeper) SendVSCPacketsToChain(ctx sdk.Context, consumerId, clientId string) error {
	if k.channelKeeperV2 == nil {
		k.Logger(ctx).Debug("IBC v2 channel keeper not configured, skipping send",
			"consumerId", consumerId,
		)
		return nil
	}

	timeoutPeriod := k.GetVAASTimeoutPeriod(ctx)
	if timeoutPeriod > channeltypesv2.MaxTimeoutDelta {
		timeoutPeriod = channeltypesv2.MaxTimeoutDelta
	}
	timeoutTimestamp := uint64(ctx.BlockTime().Add(timeoutPeriod).Unix())

	pendingPackets := k.GetPendingVSCPackets(ctx, consumerId)
	for _, data := range pendingPackets {
		payload := channeltypesv2.NewPayload(
			vaastypes.ProviderAppID,
			vaastypes.ConsumerAppID,
			"vaas-v1",
			"application/json",
			data.GetBytes(),
		)

		msg := channeltypesv2.NewMsgSendPacket(
			clientId,
			timeoutTimestamp,
			k.authority,
			payload,
		)

		resp, err := k.channelKeeperV2.SendPacket(ctx, msg)
		if err != nil {
			if errors.Is(err, clienttypes.ErrClientNotActive) {
				k.Logger(ctx).Info("IBC client expired, cannot send VSC, leaving packet data stored:",
					"consumerId", consumerId,
					"clientId", clientId,
					"vscid", data.ValsetUpdateId,
				)
				return nil
			}

			k.Logger(ctx).Error("cannot send VSC, removing consumer:",
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

		k.Logger(ctx).Info("VSCPacket sent:",
			"consumerId", consumerId,
			"clientId", clientId,
			"vscid", data.ValsetUpdateId,
			"sequence", resp.Sequence,
		)
	}
	k.DeletePendingVSCPackets(ctx, consumerId)

	return nil
}

// QueueVSCPackets queues latest validator updates for every consumer chain
// with the IBC client created.
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
