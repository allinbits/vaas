// Package govadapter presents the x/gov keeper to the provider through the
// GovKeeper interface the provider depends on, so the provider keeper names
// no gov type and its tests can mock the dependency like every other one.
package govadapter

import (
	"context"
	"errors"
	"time"

	"cosmossdk.io/collections"

	govkeeper "github.com/cosmos/cosmos-sdk/x/gov/keeper"
	govv1 "github.com/cosmos/cosmos-sdk/x/gov/types/v1"

	vaastypes "github.com/allinbits/vaas/x/vaas/types"
)

// Adapter reads proposals from a gov keeper.
type Adapter struct {
	gov *govkeeper.Keeper
}

var _ vaastypes.GovKeeper = Adapter{}

// New wraps the gov keeper.
func New(gov *govkeeper.Keeper) Adapter {
	return Adapter{gov: gov}
}

// IterateActiveProposals walks gov's active-proposals queue, which holds
// exactly the proposals in their voting period, and hands each proposal to cb
// in queue order. A queue entry whose proposal is missing is skipped rather
// than ending the walk, so one broken entry cannot hide the live votes behind
// it from the caller.
func (a Adapter) IterateActiveProposals(ctx context.Context, cb func(proposal govv1.Proposal) (stop bool, err error)) error {
	return a.gov.ActiveProposalsQueue.Walk(ctx, nil, func(key collections.Pair[time.Time, uint64], _ uint64) (bool, error) {
		proposal, err := a.gov.Proposals.Get(ctx, key.K2())
		if errors.Is(err, collections.ErrNotFound) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		return cb(proposal)
	})
}

// Proposal returns the proposal with the given id.
func (a Adapter) Proposal(ctx context.Context, proposalId uint64) (govv1.Proposal, error) {
	return a.gov.Proposals.Get(ctx, proposalId)
}
