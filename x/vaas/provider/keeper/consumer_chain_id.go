package keeper

import (
	"fmt"

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
// misbehaviour is accepted for, and it is locked at launch; the client's
// follows a client recovery (ibc-go copies the substitute's). A consumer that
// renamed itself, with the client then recovered onto the new name, is
// therefore one the provider can no longer judge: it is not served, and not
// resumed, until governance renames it too (UpdateConsumer by the authority).
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

// consumerAccusationsUndisprovable returns a per-sweep memo of whether a
// consumer's declared client tracks another chain id. The block sweeps leave
// such a consumer's pending downtime slashes and withheld fee records alone:
// no challenge header can disprove them until governance renames the
// consumer, and the rename discards them (RenameLaunchedConsumer).
func (k Keeper) consumerAccusationsUndisprovable(ctx sdk.Context) func(consumerId uint64) bool {
	memo := map[uint64]bool{}
	return func(consumerId uint64) bool {
		if undisprovable, seen := memo[consumerId]; seen {
			return undisprovable
		}
		undisprovable := false
		if clientId, found := k.GetConsumerClientId(ctx, consumerId); found {
			_, undisprovable = k.consumerClientChainIdMismatch(ctx, consumerId, clientId)
		}
		memo[consumerId] = undisprovable
		return undisprovable
	}
}

// validateConsumerClientTrustingPeriod refuses a client whose trusting period
// cannot cover the downtime challenge horizon: every accepted downtime
// accusation must stay disprovable by a header the client can still verify.
// Checked at declaration and again at resume, since a client recovery copies
// the substitute's trusting period.
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
// governance recovered the declared client onto a substitute that tracks the
// new id, and the provider's own record follows it here, in lockstep. The new
// id must be the one the declared client tracks, and free. The old id is kept
// with the client's height at the rename, so double-voting evidence from a
// block at or below that height, signed over the old id, is still accepted
// (SubmitConsumerDoubleVoting). The consumer's pending downtime accusations
// are discarded instead: a challenge header is verified by the client over
// the chain id it now tracks, so none signed over the old id can disprove
// them any more, and an accusation that cannot be disproved does not stand
// (DiscardConsumerAccusations, as after a pause that discredits the source).
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
		if h := int64(p.Boundary.RevisionHeight); h > height { //nolint:gosec // a client height is far below the int64 range
			height = h
		}
	}
	return height, len(previous) > 0, nil
}

// chainIdIsAPreviousOne reports whether any consumer carried chainId before a
// rename: it stays an evidence domain of that consumer until its deletion,
// so no other consumer may register it meanwhile (ChainIdInUse).
func (k Keeper) chainIdIsAPreviousOne(ctx sdk.Context, chainId string) (bool, error) {
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
