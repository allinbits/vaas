package keeper

import (
	"encoding/json"
	"fmt"

	"github.com/allinbits/vaas/x/vaas/consumer/types"
	vaastypes "github.com/allinbits/vaas/x/vaas/types"

	channeltypesv2 "github.com/cosmos/ibc-go/v10/modules/core/04-channel/v2/types"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

// QueueEvidencePacket queues an evidence packet to be sent to the provider chain.
// The packet is keyed by the validator's consensus address, so at most one
// pending packet exists per validator.
func (k Keeper) QueueEvidencePacket(ctx sdk.Context, packet vaastypes.EvidencePacketData) error {
	bz, err := json.Marshal(&packet)
	if err != nil {
		return fmt.Errorf("failed to marshal evidence packet: %w", err)
	}

	if err := k.PendingEvidencePackets.Set(ctx, packet.ValidatorAddr, bz); err != nil {
		return fmt.Errorf("failed to store evidence packet: %w", err)
	}

	return nil
}

// SendEvidencePackets sends all pending evidence packets to the provider chain.
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

	var keysToDelete [][]byte
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
