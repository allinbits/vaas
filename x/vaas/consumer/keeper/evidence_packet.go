package keeper

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/allinbits/vaas/x/vaas/consumer/types"
	vaastypes "github.com/allinbits/vaas/x/vaas/types"

	channeltypesv2 "github.com/cosmos/ibc-go/v10/modules/core/04-channel/v2/types"

	"cosmossdk.io/collections"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

// pendingEvidenceKey is the store key for a queued evidence packet: the
// validator's consensus address paired with the window-end height it reports.
// Keying by window (not by validator alone) lets a validator hold one pending
// packet per downtime window, so evidence for a later window is not lost while
// an earlier window's packet is still waiting to be relayed.
func pendingEvidenceKey(packet vaastypes.EvidencePacketData) collections.Pair[[]byte, int64] {
	return collections.Join([]byte(packet.ValidatorAddr), packet.WindowEndHeight)
}

// QueueEvidencePacket queues an evidence packet to be sent to the provider
// chain. The packet is keyed by (validator consensus address, window-end
// height): at most one pending packet exists per validator per window, and
// re-queuing the same window is idempotent.
func (k Keeper) QueueEvidencePacket(ctx sdk.Context, packet vaastypes.EvidencePacketData) error {
	bz, err := json.Marshal(&packet)
	if err != nil {
		return fmt.Errorf("failed to marshal evidence packet: %w", err)
	}

	if err := k.PendingEvidencePackets.Set(ctx, pendingEvidenceKey(packet), bz); err != nil {
		return fmt.Errorf("failed to store evidence packet: %w", err)
	}

	return nil
}

// RequeueEvidencePacket puts a previously-sent evidence packet back on the
// pending queue from its wire payload, used when the packet timed out
// (non-delivery). A sent packet is removed from the queue once SendPacket
// commits it, but a commitment is not delivery: re-queuing a timed-out packet
// retries delivery rather than silently dropping the evidence. The evidence
// content travels in the payload, so no separate in-flight record is needed.
// Keying is by window, so re-queuing is idempotent and never collides with a
// different window's pending packet.
func (k Keeper) RequeueEvidencePacket(ctx sdk.Context, payload []byte) error {
	var packet vaastypes.EvidencePacketData
	if err := json.Unmarshal(payload, &packet); err != nil {
		return fmt.Errorf("failed to unmarshal evidence packet for requeue: %w", err)
	}
	if err := packet.Validate(); err != nil {
		return fmt.Errorf("refusing to requeue invalid evidence packet: %w", err)
	}
	return k.QueueEvidencePacket(ctx, packet)
}

// DropRejectedEvidencePacket surfaces and discards a provider rejection of a
// previously-sent evidence packet, used when the packet came back with an
// error acknowledgement. The provider error-acks only after evaluating and
// rejecting that exact packet (unacceptable echoed params, missed count below
// threshold, unknown or renamed validator, window too old, window already
// accepted, or below the pruned acceptance floor); every one of those is
// permanent for a given packet, so re-sending the identical evidence could
// never be accepted and would loop forever. The packet was already removed
// from the queue when it was sent, so this only emits an event surfacing the
// rejection (validator, window-end height, ack error) and deliberately does
// not re-queue.
func (k Keeper) DropRejectedEvidencePacket(ctx sdk.Context, payload []byte, ackErr []byte) {
	validatorAddr := ""
	windowEndHeight := int64(0)
	var packet vaastypes.EvidencePacketData
	if err := json.Unmarshal(payload, &packet); err != nil {
		k.Logger(ctx).Error("evidence packet rejected by provider (undecodable payload); dropping",
			"error", err,
			"ack_error", hex.EncodeToString(ackErr),
		)
	} else {
		validatorAddr = packet.ValidatorAddr.String()
		windowEndHeight = packet.WindowEndHeight
		k.Logger(ctx).Error("evidence packet rejected by provider; dropping without retry",
			"validator", validatorAddr,
			"window_end_height", windowEndHeight,
			"ack_error", hex.EncodeToString(ackErr),
		)
	}

	ctx.EventManager().EmitEvent(
		sdk.NewEvent(
			vaastypes.EventTypeConsumerEvidenceRejected,
			sdk.NewAttribute(sdk.AttributeKeyModule, types.ModuleName),
			sdk.NewAttribute(vaastypes.AttributeValidatorAddress, validatorAddr),
			sdk.NewAttribute(vaastypes.AttributeWindowEndHeight, strconv.FormatInt(windowEndHeight, 10)),
			sdk.NewAttribute(vaastypes.AttributeKeyAckError, hex.EncodeToString(ackErr)),
		),
	)
}

// SendEvidencePackets sends all pending evidence packets to the provider
// chain. A packet is removed from the queue once SendPacket commits it, so it
// is not re-sent every block while in flight. Delivery is not guaranteed at
// that point: if the packet later times out (non-delivery) the consumer's
// OnTimeoutPacket re-queues it for another attempt (see RequeueEvidencePacket),
// whereas a provider error acknowledgement -- a permanent rejection of that
// exact evidence -- is surfaced as an event and dropped rather than retried
// (see DropRejectedEvidencePacket). A SendPacket error (e.g. an expired
// provider client) leaves the packet queued so it is retried on a later block.
func (k Keeper) SendEvidencePackets(ctx sdk.Context) error {
	providerClientID, found := k.GetProviderClientID(ctx)
	if !found {
		return nil
	}

	iter, err := k.PendingEvidencePackets.Iterate(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to iterate pending evidence packets: %w", err)
	}
	defer iter.Close()

	if !iter.Valid() {
		return nil
	}

	var keysToDelete []collections.Pair[[]byte, int64]
	for ; iter.Valid(); iter.Next() {
		kv, err := iter.KeyValue()
		if err != nil {
			continue
		}

		var evidencePacket vaastypes.EvidencePacketData
		if err := json.Unmarshal(kv.Value, &evidencePacket); err != nil {
			k.Logger(ctx).Error("failed to unmarshal evidence packet", "error", err)
			keysToDelete = append(keysToDelete, kv.Key)
			continue
		}

		// kv.Value is already the JSON-serialised evidence packet, use it directly.
		payload := channeltypesv2.NewPayload(
			vaastypes.ConsumerAppID,
			vaastypes.ProviderAppID,
			"vaas-v1",
			"application/json",
			kv.Value,
		)

		timeoutPeriod := min(k.GetVAASTimeoutPeriod(ctx), channeltypesv2.MaxTimeoutDelta)
		timeoutTimestamp := uint64(ctx.BlockTime().Add(timeoutPeriod).Unix())

		msg := channeltypesv2.NewMsgSendPacket(
			providerClientID,
			timeoutTimestamp,
			k.authority,
			payload,
		)

		resp, err := k.channelKeeperV2.SendPacket(ctx, msg)
		if err != nil {
			k.Logger(ctx).Error("failed to send evidence packet",
				"error", err,
				"validator", evidencePacket.ValidatorAddr.String(),
			)
			continue
		}

		k.Logger(ctx).Info("evidence packet sent",
			"sequence", resp.Sequence,
			"validator", evidencePacket.ValidatorAddr.String(),
			"infraction", evidencePacket.Infraction.String(),
			"window_end_height", evidencePacket.WindowEndHeight,
		)

		keysToDelete = append(keysToDelete, kv.Key)

		ctx.EventManager().EmitEvent(
			sdk.NewEvent(
				vaastypes.EventTypeConsumerEvidenceRequest,
				sdk.NewAttribute(sdk.AttributeKeyModule, types.ModuleName),
				sdk.NewAttribute(vaastypes.AttributeValidatorAddress, evidencePacket.ValidatorAddr.String()),
				sdk.NewAttribute(vaastypes.AttributeWindowEndHeight, fmt.Sprintf("%d", evidencePacket.WindowEndHeight)),
				sdk.NewAttribute(vaastypes.AttributeInfractionType, evidencePacket.Infraction.String()),
			),
		)
	}

	for _, key := range keysToDelete {
		if err := k.PendingEvidencePackets.Remove(ctx, key); err != nil {
			k.Logger(ctx).Error("failed to delete sent evidence packet", "error", err)
		}
	}

	return nil
}

// GetPendingEvidencePacketCount returns the number of pending evidence packets.
func (k Keeper) GetPendingEvidencePacketCount(ctx sdk.Context) int {
	iter, err := k.PendingEvidencePackets.Iterate(ctx, nil)
	if err != nil {
		return 0
	}
	defer iter.Close()

	count := 0
	for ; iter.Valid(); iter.Next() {
		count++
	}
	return count
}
