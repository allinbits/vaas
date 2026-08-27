package keeper

import (
	"errors"
	"fmt"

	"github.com/allinbits/vaas/x/vaas/consumer/types"

	abci "github.com/cometbft/cometbft/abci/types"

	ibchost "github.com/cosmos/ibc-go/v10/modules/core/exported"

	"cosmossdk.io/collections"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

func (k Keeper) InitGenesis(ctx sdk.Context, state *types.GenesisState) []abci.ValidatorUpdate {
	// PreVAAS is true during the process of a standalone to consumer changeover.
	// At the PreVAAS point in the process, the standalone chain has just been upgraded to include
	// the consumer VAAS module, but the standalone staking keeper is still managing the validator set.
	// Once the provider validator set starts validating blocks, the consumer VAAS module
	// will take over proof of stake capabilities, but the standalone staking keeper will
	// stick around for slashing/jailing purposes.
	if state.PreVAAS {
		k.SetPreVAASTrue(ctx)
		k.MarkAsPrevStandaloneChain(ctx)
		k.SetInitialValSet(ctx, state.Provider.InitialValSet)
	}
	k.SetInitGenesisHeight(ctx, ctx.BlockHeight())

	k.SetParams(ctx, state.Params)
	if !state.Params.Enabled {
		return nil
	}

	if state.NewChain {
		clientStateBytes, err := state.Provider.ClientState.Marshal()
		if err != nil {
			panic(err)
		}
		consensusStateBytes, err := state.Provider.ConsensusState.Marshal()
		if err != nil {
			panic(err)
		}

		cid, err := k.clientKeeper.CreateClient(ctx, ibchost.Tendermint, clientStateBytes, consensusStateBytes)
		if err != nil {
			panic(err)
		}

		k.SetProviderClientID(ctx, cid)
		k.SetHeightValsetUpdateID(ctx, uint64(ctx.BlockHeight()), uint64(0))

		// Pre-pin the provider chain id from the client state we were just
		// handed at genesis, rather than waiting for the first VSC packet to
		// establish it (see authenticateProviderChainID in relay.go). This
		// narrows the window during which no pin exists at all.
		k.SetProviderChainId(ctx, state.Provider.ClientState.ChainId)

		k.Logger(ctx).Info("create new provider chain client",
			"client id", cid,
		)
	} else {
		for _, h2v := range state.HeightToValsetUpdateId {
			k.SetHeightValsetUpdateID(ctx, h2v.Height, h2v.ValsetUpdateId)
		}
		k.SetProviderClientID(ctx, state.ProviderClientId)

		// Restore the pinned provider chain id on restart (see ExportGenesis):
		// without this, a state-export restart drops the pin entirely until
		// the next VSC packet lazily re-establishes it (see
		// authenticateProviderChainID in relay.go), leaving a window with no
		// pin and no re-validation of the client that was actually trusted
		// before the restart.
		if state.ProviderChainId != "" {
			k.SetProviderChainId(ctx, state.ProviderChainId)
		}
	}

	if state.PreVAAS {
		return []abci.ValidatorUpdate{}
	}

	// Restore the VSC staleness clock on a restart (see ExportGenesis); absent
	// (new chain / never received a VSC) leaves the never-stale default.
	if state.LastVscRecvTime != nil {
		k.SetLastVSCRecvTime(ctx, *state.LastVscRecvTime)
	}

	// Restore downtime-detection state for the window currently in progress
	// (see ExportGenesis); empty for a new chain or right after a window
	// closes.
	for _, entry := range state.MissedBlockBitmaps {
		if err := k.MissedBlockBitmaps.Set(ctx, entry.Addr, entry.Bitmap); err != nil {
			panic(fmt.Errorf("init: set missed block bitmap for %x: %w", entry.Addr, err))
		}
	}
	for _, entry := range state.FirstTrackedHeights {
		if err := k.FirstTrackedHeights.Set(ctx, entry.Addr, entry.Height); err != nil {
			panic(fmt.Errorf("init: set first tracked height for %x: %w", entry.Addr, err))
		}
	}
	// Restore downtime evidence packets queued but not yet sent to the
	// provider (see ExportGenesis); closing a window clears the source
	// bitmaps, so the queue is the only remaining copy of the evidence.
	for _, entry := range state.PendingEvidencePackets {
		if err := k.PendingEvidencePackets.Set(ctx, entry.Addr, entry.Packet); err != nil {
			panic(fmt.Errorf("init: set pending evidence packet for %x: %w", entry.Addr, err))
		}
	}
	if state.StagedDowntimeParams != nil {
		// Halt InitChain on unusable staged params rather than store them:
		// this write bypasses StageDowntimeParams' validDowntimeParams
		// filter, and applyStagedDowntimeParams copies whatever is staged
		// into the consumer params at the next window close, where the store
		// round-trip turns a nil MinSignedPerWindow into an explicit zero
		// and silently disables downtime detection.
		if !validDowntimeParams(*state.StagedDowntimeParams) {
			panic(fmt.Errorf(
				"init: invalid staged downtime params: signed_blocks_window %d, min_signed_per_window %s (want positive window and fraction in (0, 1))",
				state.StagedDowntimeParams.SignedBlocksWindow, state.StagedDowntimeParams.MinSignedPerWindow,
			))
		}
		if err := k.StagedDowntimeParams.Set(ctx, *state.StagedDowntimeParams); err != nil {
			panic(fmt.Errorf("init: set staged downtime params: %w", err))
		}
	}

	// populate cross chain validators states with initial valset
	k.ApplyCCValidatorChanges(ctx, state.Provider.InitialValSet)
	return state.Provider.InitialValSet
}

// ExportGenesis returns the CCV consumer module's exported genesis
func (k Keeper) ExportGenesis(ctx sdk.Context) (genesis *types.GenesisState) {
	params := k.GetConsumerParams(ctx)
	if !params.Enabled {
		return types.DefaultGenesisState()
	}

	// export the current validator set
	valset := k.MustGetCurrentValidatorsAsABCIUpdates(ctx)

	clientID, ok := k.GetProviderClientID(ctx)
	if !ok {
		return types.DefaultGenesisState()
	}

	genesis = types.NewRestartGenesisState(
		clientID,
		valset,
		k.GetAllHeightToValsetUpdateIDs(ctx),
		params,
	)

	// Preserve the pinned provider chain id across a restart (see
	// InitGenesis); absent when no pin has ever been established (e.g. a
	// PreVAAS chain that has not launched yet).
	if chainId, ok := k.GetProviderChainId(ctx); ok {
		genesis.ProviderChainId = chainId
	}

	// Preserve the VSC staleness clock across a restart (see IsVSCStale): export
	// the last-VSC-recv time only when actually recorded, so a consumer that has
	// not received a VSC keeps the absent-default (never stale) on import.
	has, err := k.LastVSCRecvTime.Has(ctx)
	if err != nil {
		panic(err)
	}
	if has {
		t := k.GetLastVSCRecvTime(ctx)
		genesis.LastVscRecvTime = &t
	}

	// Preserve downtime-detection state for the window currently in progress
	// across a restart: the missed-block bitmaps and first-tracked heights
	// accumulated so far this window, and any downtime params staged to take
	// effect at the next window boundary.
	genesis.MissedBlockBitmaps = k.exportMissedBlockBitmaps(ctx)
	genesis.FirstTrackedHeights = k.exportFirstTrackedHeights(ctx)

	// Preserve downtime evidence packets queued but not yet sent to the
	// provider: closing a window clears the source bitmaps, so the queue is
	// the only remaining copy of the evidence.
	genesis.PendingEvidencePackets = k.exportPendingEvidencePackets(ctx)
	if staged, err := k.StagedDowntimeParams.Get(ctx); err == nil {
		stagedCopy := staged
		genesis.StagedDowntimeParams = &stagedCopy
	} else if !errors.Is(err, collections.ErrNotFound) {
		panic(fmt.Errorf("export: failed to read staged downtime params: %w", err))
	}

	return genesis
}

// exportMissedBlockBitmaps walks MissedBlockBitmaps into the flat list
// carried by genesis.
func (k Keeper) exportMissedBlockBitmaps(ctx sdk.Context) []types.MissedBlockBitmapEntry {
	var entries []types.MissedBlockBitmapEntry
	iter, err := k.MissedBlockBitmaps.Iterate(ctx, nil)
	if err != nil {
		panic(fmt.Errorf("export: failed to iterate missed block bitmaps: %w", err))
	}
	defer iter.Close()
	for ; iter.Valid(); iter.Next() {
		kv, err := iter.KeyValue()
		if err != nil {
			panic(fmt.Errorf("export: failed to read missed block bitmap: %w", err))
		}
		entries = append(entries, types.MissedBlockBitmapEntry{Addr: kv.Key, Bitmap: kv.Value})
	}
	return entries
}

// exportFirstTrackedHeights walks FirstTrackedHeights into the flat list
// carried by genesis.
func (k Keeper) exportFirstTrackedHeights(ctx sdk.Context) []types.FirstTrackedHeightEntry {
	var entries []types.FirstTrackedHeightEntry
	iter, err := k.FirstTrackedHeights.Iterate(ctx, nil)
	if err != nil {
		panic(fmt.Errorf("export: failed to iterate first tracked heights: %w", err))
	}
	defer iter.Close()
	for ; iter.Valid(); iter.Next() {
		kv, err := iter.KeyValue()
		if err != nil {
			panic(fmt.Errorf("export: failed to read first tracked height: %w", err))
		}
		entries = append(entries, types.FirstTrackedHeightEntry{Addr: kv.Key, Height: kv.Value})
	}
	return entries
}

// exportPendingEvidencePackets walks PendingEvidencePackets into the flat
// list carried by genesis, each entry holding the store key (validator
// consensus address) and the JSON-encoded packet value verbatim.
func (k Keeper) exportPendingEvidencePackets(ctx sdk.Context) []types.PendingEvidencePacketEntry {
	var entries []types.PendingEvidencePacketEntry
	iter, err := k.PendingEvidencePackets.Iterate(ctx, nil)
	if err != nil {
		panic(fmt.Errorf("export: failed to iterate pending evidence packets: %w", err))
	}
	defer iter.Close()
	for ; iter.Valid(); iter.Next() {
		kv, err := iter.KeyValue()
		if err != nil {
			panic(fmt.Errorf("export: failed to read pending evidence packet: %w", err))
		}
		entries = append(entries, types.PendingEvidencePacketEntry{Addr: kv.Key, Packet: kv.Value})
	}
	return entries
}
