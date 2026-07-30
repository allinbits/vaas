package keeper

import (
	"bytes"
	"errors"
	"fmt"

	providertypes "github.com/allinbits/vaas/x/vaas/provider/types"
	vaastypes "github.com/allinbits/vaas/x/vaas/types"

	abci "github.com/cometbft/cometbft/abci/types"

	clienttypes "github.com/cosmos/ibc-go/v10/modules/core/02-client/types"
	channeltypesv2 "github.com/cosmos/ibc-go/v10/modules/core/04-channel/v2/types"
	ibcexported "github.com/cosmos/ibc-go/v10/modules/core/exported"
	ibctmtypes "github.com/cosmos/ibc-go/v10/modules/light-clients/07-tendermint"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

func (k Keeper) OnAcknowledgementPacketV2(ctx sdk.Context, sourceClientID string, ackVscId uint64, ackError string) error {
	consumerId, found := k.GetClientIdToConsumerId(ctx, sourceClientID)
	if !found {
		// ibc-go only delivers acknowledgements for packets this chain actually
		// sent (the stored packet commitment is checked before the callback
		// runs), and the provider only ever sends over tracked clients. An
		// unknown client here therefore means the consumer was removed after
		// the packet went out: a stale-but-honest delivery. Log and succeed --
		// failing would fail the relayer's whole tx over a packet nobody
		// tracks anymore.
		k.Logger(ctx).Info("recv acknowledgement on unknown client, ignoring",
			"clientID", sourceClientID, "error", ackError)
		return nil
	}

	if ackError != "" {
		k.Logger(ctx).Error("recv ErrorAcknowledgement, retrying next epoch; liveness sweep owns removal",
			"clientID", sourceClientID, "consumerId", consumerId, "error", ackError)
		return nil
	}

	if err := k.SetConsumerLastAckTime(ctx, consumerId, ctx.BlockTime()); err != nil {
		return err
	}
	if ackVscId > k.GetConsumerHighestAckedVscId(ctx, consumerId) {
		k.SetConsumerHighestAckedVscId(ctx, consumerId, ackVscId)
	}
	return nil
}

func (k Keeper) OnTimeoutPacketV2(ctx sdk.Context, sourceClientID string) error {
	consumerId, found := k.GetClientIdToConsumerId(ctx, sourceClientID)
	if !found {
		// Same reasoning as OnAcknowledgementPacketV2: a timeout can only be
		// proven for a packet this chain sent, so an unknown client means the
		// consumer is gone. Log-only, so the relayer's tx is not failed over a
		// stale delivery.
		k.Logger(ctx).Info("packet timeout on unknown client, ignoring", "clientID", sourceClientID)
		return nil
	}
	k.Logger(ctx).Info("packet timeout, retrying next epoch; liveness sweep owns removal",
		"consumerId", consumerId, "clientId", sourceClientID)
	return nil
}

// EndBlockVSU contains the EndBlock logic needed for
// the Validator Set Update sub-protocol
func (k Keeper) EndBlockVSU(ctx sdk.Context) ([]abci.ValidatorUpdate, error) {
	valUpdates, err := k.ProviderValidatorUpdates(ctx)
	if err != nil {
		return []abci.ValidatorUpdate{}, fmt.Errorf("computing the provider consensus validator set: %w", err)
	}

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

// ProviderValidatorUpdates returns changes in the provider consensus validator set
// from the last block to the current one.
// It retrieves the bonded validators from the staking module and creates a `ConsumerValidator` object for each validator.
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
	for _, val := range bondedValidators {
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
	for _, consumerId := range k.GetAllLaunchedConsumerIds(ctx) {
		clientID, _ := k.GetConsumerClientId(ctx, consumerId)
		clientID = k.discoverActiveConsumerClient(ctx, consumerId, clientID)
		if clientID == "" {
			continue
		}

		if err := k.SendVSCPacketsToChain(ctx, consumerId, clientID); err != nil {
			return fmt.Errorf("sending VSCPacket to consumer, consumerId(%d): %w", consumerId, err)
		}
	}
	return nil
}

// discoverActiveConsumerClient returns the IBC client the provider uses to
// reach the consumer.
//
// Once a client has been adopted for the consumer, it is returned
// unconditionally: the binding never moves again, no matter the client's
// status. An expired or frozen adopted client halts VSC traffic (sends fail
// and stay queued; the liveness sweep eventually removes a consumer that
// never resumes acknowledging) instead of reopening adoption -- anyone can
// permissionlessly create a client for a chain that reuses the consumer's
// chain id, so re-running discovery on client death would hand a look-alike
// chain a standing opportunity to capture the binding. Recovering a dead
// client is governance's job via ibc-go's MsgRecoverClient, which substitutes
// fresh client state under the SAME client id: the binding survives recovery
// unchanged, even though the client's latest height may jump arbitrarily.
//
// While no client was ever adopted, each call scans for a candidate and
// adopts one only if it proves by content to track the chain the provider
// itself launched: the candidate must be an Active tendermint client of the
// consumer's chain id with a registered counterparty, whose latest consensus
// state carries the CometBFT hash of the validator set the provider most
// recently computed for this consumer -- or of the set before that, since the
// consumer keeps running the previous set until the latest VSC packet is
// delivered. The chain id string is trivially copied by an attacker, but
// these hashes are not: producing a header carrying them requires the very
// validators the provider put in charge of the consumer to have signed it. A
// same-chain-id client that fails the content check is logged at warn level
// and skipped. If several candidates verify, the one with the highest latest
// height wins; if none does, nothing is adopted and discovery retries at the
// next epoch boundary (fail closed).
func (k Keeper) discoverActiveConsumerClient(ctx sdk.Context, consumerId uint64, currentClientID string) string {
	if currentClientID != "" {
		return currentClientID
	}

	chainID, err := k.GetConsumerChainId(ctx, consumerId)
	if err != nil {
		return ""
	}

	expectedHashes := k.expectedConsumerValSetHashes(ctx, consumerId)
	if len(expectedHashes) == 0 {
		k.Logger(ctx).Error("no consumer validator set hash available to verify candidate clients against, skipping discovery",
			"consumerId", consumerId)
		return ""
	}

	var bestClient string
	var bestHeight uint64

	k.clientKeeper.IterateClientStates(ctx, nil, func(clientID string, cs ibcexported.ClientState) bool {
		tmCS, ok := cs.(*ibctmtypes.ClientState)
		if !ok || tmCS.ChainId != chainID {
			return false
		}
		if k.clientKeeper.GetClientStatus(ctx, clientID) != ibcexported.Active {
			return false
		}
		cp, found := k.clientV2Keeper.GetClientCounterparty(ctx, clientID)
		if !found || cp.ClientId == "" {
			return false
		}
		if !k.clientCarriesExpectedValSetHash(ctx, clientID, tmCS.LatestHeight, expectedHashes) {
			k.Logger(ctx).Warn("client matches the consumer chain id but its consensus state does not carry a validator set this provider sent; ignoring look-alike client",
				"consumerId", consumerId,
				"chainId", chainID,
				"clientId", clientID,
			)
			return false
		}
		height := tmCS.LatestHeight.RevisionHeight
		if height > bestHeight {
			bestHeight = height
			bestClient = clientID
		}
		return false
	})

	if bestClient != "" {
		k.Logger(ctx).Info("adopting content-verified consumer client",
			"consumerId", consumerId,
			"clientId", bestClient,
		)
		k.SetConsumerClientId(ctx, consumerId, bestClient)
		return bestClient
	}
	return ""
}

// expectedConsumerValSetHashes returns the CometBFT hashes a genuine client
// of the consumer chain may currently carry in its latest consensus state:
// the hash of the validator set most recently computed for the consumer and,
// once at least one rotation has happened, the hash of the set before that
// (still running on the consumer while the latest VSC packet is in flight).
func (k Keeper) expectedConsumerValSetHashes(ctx sdk.Context, consumerId uint64) [][]byte {
	var hashes [][]byte

	valSet, err := k.GetConsumerValSet(ctx, consumerId)
	if err != nil {
		k.Logger(ctx).Error("failed to read consumer validator set",
			"consumerId", consumerId, "error", err.Error())
		return nil
	}
	if len(valSet) > 0 {
		currentHash, err := ComputeConsumerValSetHash(valSet)
		if err != nil {
			k.Logger(ctx).Error("failed to hash consumer validator set",
				"consumerId", consumerId, "error", err.Error())
			return nil
		}
		hashes = append(hashes, currentHash)
	}
	if prevHash, found := k.GetConsumerPrevValSetHash(ctx, consumerId); found {
		hashes = append(hashes, prevHash)
	}
	return hashes
}

// clientCarriesExpectedValSetHash reports whether the client's consensus
// state at the given height carries one of the expected validator set hashes
// in its NextValidatorsHash.
func (k Keeper) clientCarriesExpectedValSetHash(ctx sdk.Context, clientID string, height clienttypes.Height, expectedHashes [][]byte) bool {
	consState, found := k.clientKeeper.GetClientConsensusState(ctx, clientID, height)
	if !found {
		return false
	}
	tmConsState, ok := consState.(*ibctmtypes.ConsensusState)
	if !ok {
		return false
	}
	for _, expected := range expectedHashes {
		if bytes.Equal(tmConsState.NextValidatorsHash, expected) {
			return true
		}
	}
	return false
}

func (k Keeper) SendVSCPacketsToChain(ctx sdk.Context, consumerId uint64, clientId string) error {
	// EndBlock cannot fail the block over a relayer or client hiccup: any
	// send error is logged (inside sendVSCPacketsToChainStrict) and
	// swallowed here, leaving the packets queued for a retry next epoch.
	// The liveness sweep owns eventually removing a consumer that never
	// resumes acknowledging.
	_ = k.sendVSCPacketsToChainStrict(ctx, consumerId, clientId)
	return nil
}

// sendVSCPacketsToChainStrict sends every pending VSC packet for a consumer
// over the given IBC v2 client, in order, stopping at and returning the
// first send error instead of swallowing it. On success all pending packets
// have been sent and are cleared; on failure the packets from the failing
// one onward remain queued untouched, so a retry (next epoch, or a repeated
// call) resends exactly what did not go through.
//
// This is the shared implementation behind two callers with different error
// contracts: SendVSCPacketsToChain (the EndBlock wrapper above) swallows the
// error since EndBlock must not fail the block; ResumeConsumerChain calls
// this directly and propagates the error, since a resume tx that cannot
// actually deliver its forced resync snapshot must not report success.
func (k Keeper) sendVSCPacketsToChainStrict(ctx sdk.Context, consumerId uint64, clientId string) error {
	if k.channelKeeperV2 == nil {
		k.Logger(ctx).Debug("IBC v2 channel keeper not configured, skipping send",
			"consumerId", consumerId,
		)
		return fmt.Errorf("IBC v2 channel keeper not configured")
	}

	timeoutPeriod := min(k.GetVAASTimeoutPeriod(ctx), channeltypesv2.MaxTimeoutDelta)
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
				return err
			}

			k.Logger(ctx).Error("cannot send VSC, leaving packet data stored; liveness sweep owns removal",
				"consumerId", consumerId, "clientId", clientId, "vscid", data.ValsetUpdateId, "err", err.Error())
			return err
		}

		k.Logger(ctx).Info("VSCPacket sent:",
			"consumerId", consumerId,
			"clientId", clientId,
			"vscid", data.ValsetUpdateId,
			"sequence", resp.Sequence,
		)
		if data.ValsetUpdateId > k.GetConsumerHighestSentVscId(ctx, consumerId) {
			k.SetConsumerHighestSentVscId(ctx, consumerId, data.ValsetUpdateId)
		}
	}
	k.DeletePendingVSCPackets(ctx, consumerId)

	return nil
}

// buildVSCPacket assembles a VSC packet for a consumer, stamping the
// consumer's current debt flag, the snapshot flag, and the provider's
// current downtime params in one place for every queueing path.
func (k Keeper) buildVSCPacket(ctx sdk.Context, consumerId uint64, valUpdates []abci.ValidatorUpdate, valUpdateID uint64, isSnapshot bool) vaastypes.ValidatorSetChangePacketData {
	packet := vaastypes.NewValidatorSetChangePacketData(valUpdates, valUpdateID)
	packet.ConsumerInDebt = k.IsConsumerInDebt(ctx, consumerId)
	packet.IsSnapshot = isSnapshot
	dp := k.CurrentDowntimeParams(ctx)
	packet.DowntimeParams = &dp
	return packet
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

	for _, consumerId := range k.GetAllLaunchedConsumerIds(ctx) {

		currentValSet, err := k.GetConsumerValSet(ctx, consumerId)
		if err != nil {
			return fmt.Errorf("getting consumer current validator set, consumerId(%d): %w", consumerId, err)
		}

		// Send a full snapshot when the consumer is behind (i.e. it has
		// unacknowledged packets) or still has a packet stuck in the local
		// send queue (e.g. a client that hasn't been discovered yet, or a
		// prior send that failed and left highestSent unadvanced). Invariant:
		// never stack a diff behind an undelivered packet -- a diff assumes
		// the consumer already applied everything before it, which may never
		// hold once packets can be dropped or reordered; a snapshot converges
		// regardless of arrival order.
		isSnapshot := k.GetConsumerHighestAckedVscId(ctx, consumerId) < k.GetConsumerHighestSentVscId(ctx, consumerId) ||
			len(k.GetPendingVSCPackets(ctx, consumerId)) > 0

		valUpdates, err := k.ComputeConsumerNextValSet(ctx, bondedValidators, consumerId, currentValSet, isSnapshot)
		if err != nil {
			return fmt.Errorf("computing consumer next validator set, consumerId(%d): %w", consumerId, err)
		}

		// Always enqueue a VSC packet per launched consumer each epoch, even
		// when valUpdates is empty. This keeps the packet as the single
		// source-of-truth for the consumer's debt state: the consumer
		// receives the current ConsumerInDebt flag every epoch, so debt
		// transitions propagate at epoch boundaries without needing a
		// separate mid-epoch notification mechanism. The extra traffic is
		// bounded: at most one packet per consumer per epoch.
		packet := k.buildVSCPacket(ctx, consumerId, valUpdates, valUpdateID, isSnapshot)
		k.AppendPendingVSCPackets(ctx, consumerId, packet)
		k.Logger(ctx).Info("VSCPacket enqueued:",
			"consumerId", consumerId,
			"vscID", valUpdateID,
			"len updates", len(valUpdates),
		)
	}

	k.IncrementValidatorSetUpdateId(ctx)

	return nil
}

// QueueImmediateSnapshotVSCPacket queues a full-snapshot VSC packet for a
// single consumer outside the normal epoch cadence, sharing
// ComputeConsumerNextValSet's snapshot path with QueueVSCPackets (isSnapshot
// forced true here rather than derived from the acked/sent comparison) and
// advancing the global valset-update-id counter the same way QueueVSCPackets
// does, so the consumer's monotonic dedup logic treats it as a fresh update.
// Used where waiting for the next epoch boundary would leave the consumer with
// a set it must not act on for that long: ResumeConsumerChain, after a pause,
// and QueueConsPubKeyRotationSnapshots, after a consensus-key rotation.
func (k Keeper) QueueImmediateSnapshotVSCPacket(ctx sdk.Context, consumerId uint64) error {
	valUpdateID := k.GetValidatorSetUpdateId(ctx)

	bondedValidators, err := k.GetLastBondedValidators(ctx)
	if err != nil {
		return fmt.Errorf("getting bonded validators: %w", err)
	}

	currentValSet, err := k.GetConsumerValSet(ctx, consumerId)
	if err != nil {
		return fmt.Errorf("getting consumer current validator set, consumerId(%d): %w", consumerId, err)
	}

	valUpdates, err := k.ComputeConsumerNextValSet(ctx, bondedValidators, consumerId, currentValSet, true)
	if err != nil {
		return fmt.Errorf("computing consumer next validator set, consumerId(%d): %w", consumerId, err)
	}

	packet := k.buildVSCPacket(ctx, consumerId, valUpdates, valUpdateID, true)
	k.AppendPendingVSCPackets(ctx, consumerId, packet)
	k.Logger(ctx).Info("immediate snapshot VSCPacket enqueued:",
		"consumerId", consumerId,
		"vscID", valUpdateID,
	)

	k.IncrementValidatorSetUpdateId(ctx)

	return nil
}

// QueueConsPubKeyRotationSnapshots hands the consumers a validator's rotated
// provider consensus key right away, instead of at the next epoch boundary.
//
// A validator with no assigned consumer key on a consumer validates it with its
// provider key, so rotating that key changes the identity the consumer expects
// to sign its blocks. Left to the epoch cadence, the handover lands up to
// BlocksPerEpoch blocks late: swap the node key at rotation time and the
// validator signs with a key the consumer does not know until then, never swap
// it and the consumer counts every block it produces in that span against the
// old key. Either way it accumulates misses on every such consumer at once, and
// the downtime grace period cannot absorb them since it is anchored to the
// consumer's spawn time. So a full snapshot is queued and sent now.
//
// Only the consumers whose view actually changes are snapshotted. Where the
// validator has an assigned consumer key the consumer keeps validating under
// that key, unchanged by the rotation, and a snapshot would carry a set
// identical to the one the consumer already has -- a packet, a valset update
// id, and a relayer round trip per consumer per rotation, for no change. Those
// consumers instead have their stored validator set entry re-keyed in place by
// MigrateStateOnConsPubKeyRotation. Consumers that are not launched are left
// out too: they have no client to send over, and resuming a paused one forces
// its own snapshot (see ResumeConsumerChain).
//
// The snapshot goes out over the client already discovered for the consumer,
// without re-running client discovery: that scan belongs to the epoch path,
// which runs it for every consumer anyway, and a rotation must not pay for it.
// A consumer with no discovered client yet simply keeps the packet queued.
//
// Called from Hooks.AfterConsensusPubKeyUpdate in EndBlock, so nothing here may
// fail the block: a snapshot that cannot be computed is logged and skipped, and
// SendVSCPacketsToChain swallows a send failure, leaving the packet queued for
// the next epoch.
func (k Keeper) QueueConsPubKeyRotationSnapshots(ctx sdk.Context, newProviderAddr providertypes.ProviderConsAddress) {
	for _, consumerId := range k.GetAllLaunchedConsumerIds(ctx) {
		if _, assigned := k.GetValidatorConsumerPubKey(ctx, consumerId, newProviderAddr); assigned {
			continue
		}

		if err := k.QueueImmediateSnapshotVSCPacket(ctx, consumerId); err != nil {
			k.Logger(ctx).Error("cannot queue consensus-key rotation snapshot; consumer learns the rotated key at the next epoch",
				"consumerId", consumerId,
				"providerConsAddr", newProviderAddr.String(),
				"error", err,
			)
			continue
		}

		clientId, found := k.GetConsumerClientId(ctx, consumerId)
		if !found {
			k.Logger(ctx).Error("no client discovered for consumer; consensus-key rotation snapshot stays queued for the next epoch",
				"consumerId", consumerId,
				"providerConsAddr", newProviderAddr.String(),
			)
			continue
		}
		_ = k.SendVSCPacketsToChain(ctx, consumerId, clientId)
	}
}

// EndBlockTrackValsetUpdates records the height-to-VSC-ID mapping for the
// next block and prunes per-consumer key-assignment entries that are no
// longer reachable.
func (k Keeper) EndBlockTrackValsetUpdates(ctx sdk.Context) {
	// set the ValsetUpdateBlockHeight
	blockHeight := uint64(ctx.BlockHeight()) + 1
	valUpdateID := k.GetValidatorSetUpdateId(ctx)
	k.SetValsetUpdateBlockHeight(ctx, valUpdateID, blockHeight)
	k.Logger(ctx).Debug("vscID was mapped to block height", "vscID", valUpdateID, "height", blockHeight)

	// prune previous consumer validator addresses that are no longer needed
	for _, consumerId := range k.GetAllLaunchedConsumerIds(ctx) {
		k.PruneKeyAssignments(ctx, consumerId)
	}
}
