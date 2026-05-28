package keeper

import (
	"encoding/json"
	"fmt"

	"github.com/allinbits/vaas/x/vaas/consumer/types"
	vaastypes "github.com/allinbits/vaas/x/vaas/types"

	channeltypesv2 "github.com/cosmos/ibc-go/v10/modules/core/04-channel/v2/types"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

// QueueSlashPacket queues a slash packet to be sent to the provider chain.
// The packet is keyed by the validator's consensus address, so at most one
// pending packet exists per validator.
func (k Keeper) QueueSlashPacket(ctx sdk.Context, packet vaastypes.EvidencePacketData) error {
	bz, err := json.Marshal(&packet)
	if err != nil {
		return fmt.Errorf("failed to marshal slash packet: %w", err)
	}

	if err := k.PendingSlashPackets.Set(ctx, packet.ValidatorAddr, bz); err != nil {
		return fmt.Errorf("failed to store slash packet: %w", err)
	}

	return nil
}

// SendSlashPackets sends all pending slash packets to the provider chain.
func (k Keeper) SendSlashPackets(ctx sdk.Context) error {
	providerClientID, found := k.GetProviderClientID(ctx)
	if !found {
		return nil
	}

	iter, err := k.PendingSlashPackets.Iterate(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to iterate pending slash packets: %w", err)
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

		var slashPacket vaastypes.EvidencePacketData
		if err := json.Unmarshal(kv.Value, &slashPacket); err != nil {
			k.Logger(ctx).Error("failed to unmarshal slash packet", "error", err)
			keysToDelete = append(keysToDelete, kv.Key)
			continue
		}

		payload := channeltypesv2.NewPayload(
			vaastypes.ConsumerAppID,
			vaastypes.ProviderAppID,
			"vaas-v1",
			"application/json",
			slashPacket.GetBytes(),
		)

		timeoutPeriod := k.GetVAASTimeoutPeriod(ctx)
		timeoutTimestamp := uint64(ctx.BlockTime().Add(timeoutPeriod).Unix())

		msg := channeltypesv2.NewMsgSendPacket(
			providerClientID,
			timeoutTimestamp,
			k.authority,
			payload,
		)

		resp, err := k.channelKeeperV2.SendPacket(ctx, msg)
		if err != nil {
			k.Logger(ctx).Error("failed to send slash packet",
				"error", err,
				"validator", slashPacket.ValidatorAddr.String(),
			)
			continue
		}

		k.Logger(ctx).Info("slash packet sent",
			"sequence", resp.Sequence,
			"validator", slashPacket.ValidatorAddr.String(),
			"infraction", slashPacket.Infraction.String(),
			"infraction_height", slashPacket.InfractionHeight,
		)

		keysToDelete = append(keysToDelete, kv.Key)

		ctx.EventManager().EmitEvent(
			sdk.NewEvent(
				vaastypes.EventTypeConsumerSlashRequest,
				sdk.NewAttribute(sdk.AttributeKeyModule, types.ModuleName),
				sdk.NewAttribute(vaastypes.AttributeValidatorAddress, slashPacket.ValidatorAddr.String()),
				sdk.NewAttribute(vaastypes.AttributeInfractionHeight, fmt.Sprintf("%d", slashPacket.InfractionHeight)),
				sdk.NewAttribute(vaastypes.AttributeInfractionType, slashPacket.Infraction.String()),
			),
		)
	}

	for _, key := range keysToDelete {
		if err := k.PendingSlashPackets.Remove(ctx, key); err != nil {
			k.Logger(ctx).Error("failed to delete sent slash packet", "error", err)
		}
	}

	return nil
}

// GetPendingSlashPacketCount returns the number of pending slash packets.
func (k Keeper) GetPendingSlashPacketCount(ctx sdk.Context) int {
	iter, err := k.PendingSlashPackets.Iterate(ctx, nil)
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
