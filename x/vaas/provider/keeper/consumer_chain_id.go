package keeper

import (
	"context"
	"fmt"
	"strconv"

	clienttypes "github.com/cosmos/ibc-go/v10/modules/core/02-client/types"
	ibctmtypes "github.com/cosmos/ibc-go/v10/modules/light-clients/07-tendermint"

	"cosmossdk.io/collections"
	errorsmod "cosmossdk.io/errors"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/allinbits/vaas/x/vaas/provider/types"
	vaastypes "github.com/allinbits/vaas/x/vaas/types"
)

// consumerClientState returns the tendermint client state behind a consumer's
// declared client.
func (k Keeper) consumerClientState(ctx sdk.Context, clientId string) (*ibctmtypes.ClientState, error) {
	clientState, found := k.clientKeeper.GetClientState(ctx, clientId)
	if !found {
		return nil, errorsmod.Wrapf(types.ErrInvalidConsumerClient, "client %s has no client state", clientId)
	}
	tmClientState, ok := clientState.(*ibctmtypes.ClientState)
	if !ok {
		return nil, errorsmod.Wrapf(types.ErrInvalidConsumerClient, "client %s is not a tendermint client", clientId)
	}
	return tmClientState, nil
}

// consumerClientChainIdMismatch reports the chain id the consumer's declared
// client tracks when it is not the consumer's own. The consumer's chain id
// is what its double-voting evidence is verified over and what its
// misbehaviour is accepted for, and only governance changes it after launch;
// the client's follows an ibc-go client upgrade (MsgUpgradeClient, which
// takes the chain id from the upgraded client state the consumer committed)
// or a client recovery (MsgRecoverClient, which copies the substitute's). A
// consumer whose client tracks another chain id is one the provider can no
// longer judge: PauseConsumersWithMismatchedClients pauses it, and it is not
// resumed until the two agree again.
func (k Keeper) consumerClientChainIdMismatch(ctx sdk.Context, consumerId uint64, clientId string) (clientChainId string, mismatch bool) {
	chainId, err := k.GetConsumerChainId(ctx, consumerId)
	if err != nil {
		return "", false
	}
	tmClientState, err := k.consumerClientState(ctx, clientId)
	if err != nil {
		return "", false
	}
	if tmClientState.ChainId == chainId {
		return "", false
	}
	return tmClientState.ChainId, true
}

// PauseConsumersWithMismatchedClients pauses, under the client chain id
// mismatch reason, every launched consumer whose declared client tracks a
// chain id other than the consumer's (consumerClientChainIdMismatch). Such a
// consumer cannot be judged: its double-voting evidence is verified over the
// consumer's chain id, and a downtime challenge needs a header signed over
// the consumer's chain id that the client verifies, while the client only
// verifies headers of the chain id it tracks, so none of its pending
// downtime accusations can be disproved. The pause
// discards them and repays the fees withheld under them, as a challenge or
// light-client pause does (an accusation that cannot be disproved does not
// stand), keeps the liveness sweep away from a consumer that is silent only
// because it is not served, and gives governance MaxPauseDuration to rename
// the consumer to the client's chain id (MsgUpdateConsumer by the authority)
// or recover the client onto the consumer's, then resume it.
//
// It runs in BeginBlock before the downtime slash and withheld fee sweeps,
// so nothing of the consumer matures between the client switch and the
// pause. vaas_consumer_client_chain_id_mismatch is emitted once, with the
// pause; a consumer already paused for another reason keeps it, and its
// resume is refused while the mismatch holds. A pause that fails is logged
// and retried next block; nothing here may halt the chain.
func (k Keeper) PauseConsumersWithMismatchedClients(ctx sdk.Context) {
	for _, consumerId := range k.GetAllLaunchedConsumerIds(ctx) {
		clientId, found := k.GetConsumerClientId(ctx, consumerId)
		if !found || clientId == "" {
			continue
		}
		clientChainId, mismatch := k.consumerClientChainIdMismatch(ctx, consumerId, clientId)
		if !mismatch {
			continue
		}
		chainId, _ := k.GetConsumerChainId(ctx, consumerId)
		if err := k.PauseConsumerChain(ctx, consumerId, types.PAUSE_REASON_CLIENT_CHAIN_ID_MISMATCH); err != nil {
			k.Logger(ctx).Error("consumer client tracks another chain id but the consumer could not be paused; retrying next block",
				"consumerId", consumerId, "clientId", clientId, "clientChainId", clientChainId, "consumerChainId", chainId, "err", err.Error())
			continue
		}
		ctx.EventManager().EmitEvent(sdk.NewEvent(
			types.EventTypeConsumerClientChainIdMismatch,
			sdk.NewAttribute(sdk.AttributeKeyModule, types.ModuleName),
			sdk.NewAttribute(types.AttributeConsumerId, strconv.FormatUint(consumerId, 10)),
			sdk.NewAttribute(types.AttributeConsumerClientID, clientId),
			sdk.NewAttribute(types.AttributeClientChainId, clientChainId),
			sdk.NewAttribute(types.AttributeConsumerChainId, chainId),
		))
		k.Logger(ctx).Info("consumer client tracks another chain id: consumer paused pending a governance rename or client recovery",
			"consumerId", consumerId, "clientId", clientId, "clientChainId", clientChainId, "consumerChainId", chainId)
	}
}

// validateConsumerClientTrustingPeriod refuses a client whose trusting period
// cannot cover the downtime challenge horizon: every accepted downtime
// accusation must stay disprovable by a header the client can still verify.
// Checked at declaration, at a governance rename and at resume, since a
// client upgrade or recovery can change the trusting period.
func (k Keeper) validateConsumerClientTrustingPeriod(ctx sdk.Context, clientId string, tmClientState *ibctmtypes.ClientState) error {
	if horizon := k.GetInfractionParams(ctx).ChallengeableInterval(); tmClientState.TrustingPeriod <= horizon {
		return errorsmod.Wrapf(types.ErrInvalidMsgUpdateConsumer,
			"client %s trusting period (%s) must exceed the downtime challenge horizon (%s): every accepted downtime accusation must stay disprovable by a header this client can still verify",
			clientId, tmClientState.TrustingPeriod, horizon)
	}
	return nil
}

// RenameLaunchedConsumer is the governance half of a consumer rename after
// launch: the consumer bumped its chain id (an upgrade's revision bump, say),
// the declared client follows it through an ibc-go client upgrade or a
// governance recovery, and the provider's own record follows the client
// here, on a consumer that has launched: one still launched, one paused for
// the mismatch (PauseConsumersWithMismatchedClients), or one stopped, as a
// mismatch pause that outlived MaxPauseDuration leaves it, whose chain id
// serves the verification of its evidence and nothing else until its
// deletion. The new id must be the one the declared client tracks, and free.
// The old id is kept with the client's height at the rename, so
// double-voting evidence from a block at or below that height, signed over
// the old id, is still accepted (SubmitConsumerDoubleVoting). The consumer's
// pending downtime accusations are discarded instead: a challenge header is
// verified by the client over the chain id it now tracks, so none signed
// over the old id can disprove them any more, and an accusation that cannot
// be disproved does not stand (DiscardConsumerAccusations, as after a pause
// that discredits the source).
func (k Keeper) RenameLaunchedConsumer(ctx sdk.Context, consumerId uint64, newChainId string) error {
	clientId, found := k.GetConsumerClientId(ctx, consumerId)
	if !found {
		return errorsmod.Wrapf(types.ErrInvalidMsgUpdateConsumer,
			"consumer %d has no declared client to follow", consumerId)
	}
	tmClientState, err := k.consumerClientState(ctx, clientId)
	if err != nil {
		return err
	}
	if tmClientState.ChainId != newChainId {
		return errorsmod.Wrapf(types.ErrInvalidMsgUpdateConsumer,
			"a launched consumer is renamed to the chain id its declared client tracks: client %s tracks %s, got %s",
			clientId, tmClientState.ChainId, newChainId)
	}
	// The client an upgrade or recovery left behind is served from the
	// rename on, with or without a resume, so it meets the declaration's
	// floor like any served client; one rule for every rename, a stopped
	// consumer's included.
	if err := k.validateConsumerClientTrustingPeriod(ctx, clientId, tmClientState); err != nil {
		return err
	}
	// Free of every live chain id and of every previous one, this
	// consumer's included: a rename back to a chain id the consumer carried
	// before is refused, as that id stays its evidence domain for the blocks
	// up to its boundary.
	if err := k.ValidateConsumerChainIdFree(ctx, newChainId); err != nil {
		return err
	}
	oldChainId, err := k.GetConsumerChainId(ctx, consumerId)
	if err != nil {
		return err
	}
	if err := k.ConsumerPreviousChainIds.Set(ctx, collections.Join(consumerId, oldChainId), tmClientState.LatestHeight); err != nil {
		return fmt.Errorf("recording the previous chain id of consumer %d: %w", consumerId, err)
	}
	k.SetConsumerChainId(ctx, consumerId, newChainId)
	if err := k.DiscardConsumerAccusations(ctx, consumerId); err != nil {
		return fmt.Errorf("discarding the downtime accusations of consumer %d at its rename: %w", consumerId, err)
	}
	k.Logger(ctx).Info("consumer renamed to its client's chain id",
		"consumerId", consumerId, "from", oldChainId, "to", newChainId, "boundary", tmClientState.LatestHeight.String())
	return nil
}

// EvidenceChainIdFor returns the chain id double-voting evidence for the
// consumer may be signed over, given the chain id of the infraction block's
// header and its height: the consumer's current chain id, or a previous one
// for a block the client could have verified under it, i.e. of a revision
// below the boundary's (a chain id's revision is the suffix CometBFT parses)
// or of the boundary's revision at or below its height. The votes' signature
// binds the chain id already; the bound only closes the replay window.
func (k Keeper) EvidenceChainIdFor(ctx sdk.Context, consumerId uint64, headerChainId string, height int64) (string, error) {
	current, err := k.GetConsumerChainId(ctx, consumerId)
	if err != nil {
		return "", err
	}
	if headerChainId == current {
		return current, nil
	}
	boundary, err := k.ConsumerPreviousChainIds.Get(ctx, collections.Join(consumerId, headerChainId))
	if err == nil && height >= 0 {
		evidence := clienttypes.NewHeight(clienttypes.ParseChainID(headerChainId), uint64(height))
		if evidence.LTE(boundary) {
			return headerChainId, nil
		}
	}
	return "", errorsmod.Wrapf(vaastypes.ErrInvalidDoubleVotingEvidence,
		"infraction block header chain id (%s) is not consumer %d's chain id (%s), nor one it carried at height %d",
		headerChainId, consumerId, current, height)
}

// latestRenameBoundary returns the highest height at which the consumer was
// renamed, when it was: the declared client's height at the time, recorded
// with the previous chain id (RenameLaunchedConsumer). A downtime window
// starting at or below it is refused (HandleConsumerDowntime).
func (k Keeper) latestRenameBoundary(ctx sdk.Context, consumerId uint64) (height int64, renamed bool, err error) {
	previous, err := k.GetConsumerPreviousChainIds(ctx, consumerId)
	if err != nil {
		return 0, false, fmt.Errorf("reading the previous chain ids of consumer %d: %w", consumerId, err)
	}
	for _, p := range previous {
		if h := int64(p.Boundary.RevisionHeight); h > height {
			height = h
		}
	}
	return height, len(previous) > 0, nil
}

// chainIdIsAPreviousOne reports whether any consumer carried chainId before a
// rename: it stays an evidence domain of that consumer until its deletion,
// so no other consumer may register it meanwhile (ChainIdInUse).
func (k Keeper) chainIdIsAPreviousOne(ctx context.Context, chainId string) (bool, error) {
	found := false
	err := k.ConsumerPreviousChainIds.Walk(ctx, nil, func(key collections.Pair[uint64, string], _ clienttypes.Height) (bool, error) {
		if key.K2() == chainId {
			found = true
			return true, nil
		}
		return false, nil
	})
	return found, err
}

// GetConsumerPreviousChainIds returns the chain ids the consumer carried
// before its renames, with the client height at each rename.
func (k Keeper) GetConsumerPreviousChainIds(ctx sdk.Context, consumerId uint64) ([]types.PreviousChainId, error) {
	var out []types.PreviousChainId
	err := k.ConsumerPreviousChainIds.Walk(ctx, collections.NewPrefixedPairRange[uint64, string](consumerId),
		func(key collections.Pair[uint64, string], boundary clienttypes.Height) (bool, error) {
			out = append(out, types.PreviousChainId{ChainId: key.K2(), Boundary: boundary})
			return false, nil
		})
	return out, err
}

// DeleteConsumerPreviousChainIds clears the renames with the consumer's state.
func (k Keeper) DeleteConsumerPreviousChainIds(ctx sdk.Context, consumerId uint64) error {
	return k.ConsumerPreviousChainIds.Clear(ctx, collections.NewPrefixedPairRange[uint64, string](consumerId))
}
