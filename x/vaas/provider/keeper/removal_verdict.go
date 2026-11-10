package keeper

import (
	"fmt"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

// MarkConsumerRemovedByGovernance records that governance removed the
// consumer: either MsgRemoveConsumer stopped it, or its removal vote passed
// after it had stopped for another reason. The record is the verdict that
// voids the consumer's equivocation evidence, the pending punishments the
// removal cancels and any evidence submitted afterwards alike, so that a
// consumer governance judged untrustworthy cannot keep earning punishments
// for the validators it accused.
func (k Keeper) MarkConsumerRemovedByGovernance(ctx sdk.Context, consumerId uint64) {
	if err := k.ConsumersRemovedByGovernance.Set(ctx, consumerId); err != nil {
		panic(fmt.Errorf("recording the governance removal of consumer %d: %w", consumerId, err))
	}
}

// IsConsumerRemovedByGovernance reports whether governance removed the
// consumer (see MarkConsumerRemovedByGovernance).
func (k Keeper) IsConsumerRemovedByGovernance(ctx sdk.Context, consumerId uint64) bool {
	removed, err := k.ConsumersRemovedByGovernance.Has(ctx, consumerId)
	if err != nil {
		panic(fmt.Errorf("reading the governance removal record of consumer %d: %w", consumerId, err))
	}
	return removed
}

// DeleteConsumerRemovedByGovernance clears the record with the rest of the
// consumer's state at deletion.
func (k Keeper) DeleteConsumerRemovedByGovernance(ctx sdk.Context, consumerId uint64) {
	if err := k.ConsumersRemovedByGovernance.Remove(ctx, consumerId); err != nil {
		panic(fmt.Errorf("clearing the governance removal record of consumer %d: %w", consumerId, err))
	}
}
