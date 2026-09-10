package keeper

import (
	"time"

	"github.com/allinbits/vaas/x/vaas/provider/types"

	"cosmossdk.io/collections"

	sdk "github.com/cosmos/cosmos-sdk/types"
	govv1 "github.com/cosmos/cosmos-sdk/x/gov/types/v1"
)

// removalVote reports whether a governance proposal carrying a
// MsgRemoveConsumer for consumerId is currently in its voting period, and if
// so that proposal's id and the end of its voting period. Only the voting
// period counts: a proposal still in its deposit period shields nothing, so
// filing proposals is not a free deferral lever before the community has
// actually been asked. With several such proposals live at once the
// latest-ending one is reported, so a punishment deferred behind it cannot
// execute while any of them is still open.
//
// The scan walks the gov keeper's active-proposals queue, which holds exactly
// the proposals in voting, and decodes a proposal's message payloads only
// when their type URL is MsgRemoveConsumer's, so per-block cost stays
// proportional to the (small) number of live proposals.
func (k Keeper) removalVote(ctx sdk.Context, consumerId uint64) (proposalId uint64, votingEnd time.Time, active bool) {
	if k.removalVoteFn != nil {
		return k.removalVoteFn(ctx, consumerId)
	}
	k.mustHaveGovKeeper()

	removeConsumerTypeURL := sdk.MsgTypeURL(&types.MsgRemoveConsumer{})

	err := k.govKeeper.ActiveProposalsQueue.Walk(ctx, nil, func(key collections.Pair[time.Time, uint64], _ uint64) (bool, error) {
		proposal, err := k.govKeeper.Proposals.Get(ctx, key.K2())
		if err != nil {
			k.Logger(ctx).Error("failed to read an active governance proposal", "proposalId", key.K2(), "error", err)
			return false, nil
		}
		if proposal.Status != govv1.StatusVotingPeriod || proposal.VotingEndTime == nil {
			return false, nil
		}
		for _, msg := range proposal.Messages {
			if msg.TypeUrl != removeConsumerTypeURL {
				continue
			}
			var remove types.MsgRemoveConsumer
			if err := k.cdc.Unmarshal(msg.Value, &remove); err != nil {
				continue
			}
			if remove.ConsumerId != consumerId {
				continue
			}
			// The queue is keyed by voting end, so a later match ends no
			// earlier; on an equal end the higher id wins, deterministically.
			if !active || proposal.VotingEndTime.After(votingEnd) || (proposal.VotingEndTime.Equal(votingEnd) && proposal.Id > proposalId) {
				proposalId = proposal.Id
				votingEnd = *proposal.VotingEndTime
				active = true
			}
			break
		}
		return false, nil
	})
	if err != nil {
		k.Logger(ctx).Error("failed to scan active proposals for a removal vote", "error", err)
		return 0, time.Time{}, false
	}
	return proposalId, votingEnd, active
}

// mustHaveGovKeeper panics when no gov keeper is wired: the removal-vote
// deferral is a protocol guarantee, and an app wiring that silently disabled
// it would be a bug to surface, not a mode to run in (see also the check at
// the start of every block in the module's BeginBlock).
func (k Keeper) mustHaveGovKeeper() {
	if k.govKeeper == nil {
		panic("provider keeper: no gov keeper wired; call SetGovKeeper after constructing the gov keeper")
	}
}

// HasGovKeeper reports whether SetGovKeeper has been called.
func (k Keeper) HasGovKeeper() bool {
	return k.govKeeper != nil
}

// removalProposalStatus reports the current status of the governance proposal
// a pending punishment deferred behind, with the end of its voting period
// while it is still in voting. found is false when the proposal does not
// exist; gov never deletes a proposal that reached a
// vote, so a missing one never was one. This is how a removal vote's verdict
// reaches a punishment whose consumer had already stopped for another reason
// by the time the vote ended: the proposal's MsgRemoveConsumer cannot run
// against a stopped consumer, but its tally is on record.
func (k Keeper) removalProposalStatus(ctx sdk.Context, proposalId uint64) (status govv1.ProposalStatus, votingEnd time.Time, found bool) {
	if k.removalProposalStatusFn != nil {
		return k.removalProposalStatusFn(ctx, proposalId)
	}
	k.mustHaveGovKeeper()
	if proposalId == 0 {
		return govv1.StatusNil, time.Time{}, false
	}
	proposal, err := k.govKeeper.Proposals.Get(ctx, proposalId)
	if err != nil {
		return govv1.StatusNil, time.Time{}, false
	}
	if proposal.VotingEndTime != nil {
		votingEnd = *proposal.VotingEndTime
	}
	return proposal.Status, votingEnd, true
}

// removalVoteMemo answers removalVote once per consumer within one sweep, so
// a sweep over many matured entries of the same consumer scans the active
// proposals a single time.
type removalVoteMemo struct {
	k     Keeper
	votes map[uint64]removalVoteResult
}

type removalVoteResult struct {
	proposalId uint64
	end        time.Time
	active     bool
}

func (k Keeper) newRemovalVoteMemo() *removalVoteMemo {
	return &removalVoteMemo{k: k, votes: map[uint64]removalVoteResult{}}
}

func (m *removalVoteMemo) get(ctx sdk.Context, consumerId uint64) removalVoteResult {
	v, checked := m.votes[consumerId]
	if !checked {
		v.proposalId, v.end, v.active = m.k.removalVote(ctx, consumerId)
		m.votes[consumerId] = v
	}
	return v
}
