package keeper

import (
	"errors"
	"fmt"
	"reflect"

	"github.com/allinbits/vaas/x/vaas/provider/types"
	vaastypes "github.com/allinbits/vaas/x/vaas/types"

	abci "github.com/cometbft/cometbft/abci/types"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

// InitGenesis initializes the VAAS provider state from a GenesisState.
//
// Per-consumer state (owner, metadata, init params, removal time, client id,
// consumer genesis) is read directly from each ConsumerState, keyed by the
// canonical consumer id carried in cs.ConsumerId. The keeper's derived
// collections (spawn-time queue, removal-time queue, equivocation evidence
// min height) are then reconstructed in a second pass from the per-consumer
// fields. ConsumerDebt is left unset; the first BeginBlock after import
// re-derives it from the fee pool balance. After all consumers are imported,
// the ConsumerId sequence counter is advanced past the highest imported id
// so subsequent MsgCreateConsumer calls do not collide with the imported
// records.
func (k Keeper) InitGenesis(ctx sdk.Context, genState *types.GenesisState) []abci.ValidatorUpdate {
	k.SetValidatorSetUpdateId(ctx, genState.ValsetUpdateId)
	for _, v2h := range genState.ValsetUpdateIdToHeight {
		k.SetValsetUpdateBlockHeight(ctx, v2h.ValsetUpdateId, v2h.Height)
	}

	// First pass: write all per-consumer keeper collections under the
	// provided consumer id from each ConsumerState.
	maxConsumerId := uint64(0)
	for i := range genState.ConsumerStates {
		cs := &genState.ConsumerStates[i]
		consumerId := cs.ConsumerId

		// Track the highest imported consumer id so we can advance the keeper's
		// ConsumerId sequence past it below; future MsgCreateConsumer calls
		// then won't collide with the imported records.
		if consumerId > maxConsumerId {
			maxConsumerId = consumerId
		}

		k.SetConsumerChainId(ctx, consumerId, cs.ChainId)
		k.SetConsumerPhase(ctx, consumerId, cs.Phase)

		if cs.OwnerAddress != "" {
			k.SetConsumerOwnerAddress(ctx, consumerId, cs.OwnerAddress)
		}
		if cs.Metadata != nil {
			if err := k.SetConsumerMetadata(ctx, consumerId, *cs.Metadata); err != nil {
				panic(fmt.Errorf("init: set metadata for %d: %w", consumerId, err))
			}
		}
		if cs.InitParams != nil {
			if err := k.SetConsumerInitializationParameters(ctx, consumerId, *cs.InitParams); err != nil {
				panic(fmt.Errorf("init: set init params for %d: %w", consumerId, err))
			}
		}
		if cs.ClientId != "" {
			k.SetConsumerClientId(ctx, consumerId, cs.ClientId)
		}
		// reflect.DeepEqual is used here because ConsumerGenesisState
		// transitively references types from cometbft/ibc-go that do not
		// have a generated Equal method, so we can't enable
		// gogoproto.equal_all on its proto file.
		if !reflect.DeepEqual(cs.ConsumerGenesis, vaastypes.ConsumerGenesisState{}) {
			if err := k.SetConsumerGenesis(ctx, consumerId, cs.ConsumerGenesis); err != nil {
				panic(fmt.Errorf("init: set consumer genesis for %d: %w", consumerId, err))
			}
		}
		if cs.RemovalTime != nil {
			if err := k.SetConsumerRemovalTime(ctx, consumerId, *cs.RemovalTime); err != nil {
				panic(fmt.Errorf("init: set removal time for %d: %w", consumerId, err))
			}
		}
		if cs.PauseExpirationTime != nil {
			if err := k.SetConsumerPauseExpirationTime(ctx, consumerId, *cs.PauseExpirationTime); err != nil {
				panic(fmt.Errorf("init: set pause expiration time for %d: %w", consumerId, err))
			}
		}
		// Restore the liveness clock (see ExportGenesis): only when present, so
		// a consumer that never launched keeps the absent-defaults.
		if cs.LastAckTime != nil {
			if err := k.SetConsumerLastAckTime(ctx, consumerId, *cs.LastAckTime); err != nil {
				panic(fmt.Errorf("init: set last ack time for %d: %w", consumerId, err))
			}
		}
		if cs.HighestSentVscId != 0 {
			k.SetConsumerHighestSentVscId(ctx, consumerId, cs.HighestSentVscId)
		}
		if cs.HighestAckedVscId != 0 {
			k.SetConsumerHighestAckedVscId(ctx, consumerId, cs.HighestAckedVscId)
		}
		if len(cs.PendingValsetChanges) > 0 {
			k.AppendPendingVSCPackets(ctx, consumerId, cs.PendingValsetChanges...)
		}
	}

	// Second pass: derive queue and equivocation-min-height state from the
	// per-consumer fields just written.
	for i := range genState.ConsumerStates {
		cs := &genState.ConsumerStates[i]
		consumerId := cs.ConsumerId

		switch cs.Phase {
		case types.CONSUMER_PHASE_INITIALIZED:
			if cs.InitParams != nil {
				if err := k.AppendConsumerToBeLaunched(ctx, consumerId, cs.InitParams.SpawnTime); err != nil {
					panic(fmt.Errorf("init: enqueue spawn for %d: %w", consumerId, err))
				}
			}
		case types.CONSUMER_PHASE_LAUNCHED:
			if cs.InitParams != nil {
				k.SetEquivocationEvidenceMinHeight(ctx, consumerId, cs.InitParams.InitialHeight.RevisionHeight)
			}
		case types.CONSUMER_PHASE_STOPPED:
			if cs.InitParams != nil {
				k.SetEquivocationEvidenceMinHeight(ctx, consumerId, cs.InitParams.InitialHeight.RevisionHeight)
			}
			if cs.RemovalTime != nil {
				if err := k.AppendConsumerToBeRemoved(ctx, consumerId, *cs.RemovalTime); err != nil {
					panic(fmt.Errorf("init: enqueue removal for %d: %w", consumerId, err))
				}
			}
		case types.CONSUMER_PHASE_PAUSED:
			if cs.InitParams != nil {
				k.SetEquivocationEvidenceMinHeight(ctx, consumerId, cs.InitParams.InitialHeight.RevisionHeight)
			}
			if cs.PauseExpirationTime != nil {
				if err := k.AppendConsumerToBeAutoStopped(ctx, consumerId, *cs.PauseExpirationTime); err != nil {
					panic(fmt.Errorf("init: enqueue auto-stop for %d: %w", consumerId, err))
				}
			}
		}
	}

	// Advance the ConsumerId sequence past the highest imported id so
	// MsgCreateConsumer's next FetchAndIncrementConsumerId does not collide.
	if len(genState.ConsumerStates) > 0 {
		if err := k.ConsumerId.Set(ctx, maxConsumerId+1); err != nil {
			panic(fmt.Errorf("init: advance consumer id sequence to %d: %w", maxConsumerId+1, err))
		}
	}

	// Import per-consumer fees_per_block overrides.
	// GenesisState.Validate guarantees parseable, non-negative amounts.
	for _, ov := range genState.ConsumerFeesPerBlockOverrides {
		amt, _ := math.NewIntFromString(ov.Amount)
		if err := k.ConsumerFeesPerBlockOverride.Set(ctx, ov.ConsumerId, amt); err != nil {
			panic(fmt.Errorf("init: set fees-per-block override for %d: %w", ov.ConsumerId, err))
		}
	}

	// Import key assignment state
	for _, item := range genState.ValidatorConsumerPubkeys {
		providerAddr := types.NewProviderConsAddress(item.ProviderAddr)
		k.SetValidatorConsumerPubKey(ctx, item.ConsumerId, providerAddr, *item.ConsumerKey)
	}
	for _, item := range genState.ValidatorsByConsumerAddr {
		consumerAddr := types.NewConsumerConsAddress(item.ConsumerAddr)
		providerAddr := types.NewProviderConsAddress(item.ProviderAddr)
		k.SetValidatorByConsumerAddr(ctx, item.ConsumerId, consumerAddr, providerAddr)
	}
	for _, item := range genState.ConsumerAddrsToPrune {
		for _, addr := range item.ConsumerAddrs.Addresses {
			consumerAddr := types.NewConsumerConsAddress(addr)
			k.AppendConsumerAddrsToPrune(ctx, item.ConsumerId, item.PruneTs, consumerAddr)
		}
	}

	// Load primary share records and accumulate per-(consumer, denom) totals
	// in a single pass. GenesisState.Validate has already vetted the input.
	totals := map[uint64]map[string]math.Int{}
	for _, s := range genState.ConsumerFeePoolShares {
		addr, err := sdk.AccAddressFromBech32(s.Depositor)
		if err != nil {
			panic(fmt.Errorf("invalid depositor in genesis: %w", err))
		}
		if err := k.ConsumerFeePoolShares.Set(ctx,
			collections.Join3(s.ConsumerId, s.Denom, addr), s.Shares,
		); err != nil {
			panic(err)
		}
		if _, ok := totals[s.ConsumerId]; !ok {
			totals[s.ConsumerId] = map[string]math.Int{}
		}
		if cur, ok := totals[s.ConsumerId][s.Denom]; ok {
			totals[s.ConsumerId][s.Denom] = cur.Add(s.Shares)
		} else {
			totals[s.ConsumerId][s.Denom] = s.Shares
		}
	}
	for consumerId, byDenom := range totals {
		for denom, sum := range byDenom {
			if err := k.ConsumerFeePoolTotalShares.Set(ctx,
				collections.Join(consumerId, denom), sum,
			); err != nil {
				panic(err)
			}
		}
	}

	// Rebuild reverse-lookup from non-DELETED consumers (read phase collection)
	// and, in the same pass, check that no fee pool address holds a balance
	// without a matching share record. Bank's InitGenesis runs before ours
	// (see app.go OrderInitGenesis), so any pool balance present here was put
	// there by the genesis file itself. Such a balance with no shares would
	// be unreachable from runtime ops (only MintShares moves funds into a
	// pool, and it always credits shares), so we treat it as a malformed
	// genesis and halt rather than silently accept funds nobody can claim.
	phaseIter, err := k.ConsumerPhase.Iterate(ctx, nil)
	if err != nil {
		panic(err)
	}
	defer phaseIter.Close()
	for ; phaseIter.Valid(); phaseIter.Next() {
		consumerId, err := phaseIter.Key()
		if err != nil {
			panic(err)
		}
		phaseRaw, err := phaseIter.Value()
		if err != nil {
			panic(err)
		}
		if types.ConsumerPhase(phaseRaw) == types.CONSUMER_PHASE_DELETED {
			continue
		}
		poolAddr := k.GetConsumerFeePoolAddress(consumerId)
		if err := k.FeePoolAddressToConsumerId.Set(ctx, poolAddr, consumerId); err != nil {
			panic(err)
		}
		denomsWithShares := totals[consumerId]
		for _, coin := range k.bankKeeper.GetAllBalances(ctx, poolAddr) {
			if _, ok := denomsWithShares[coin.Denom]; !ok {
				panic(fmt.Errorf(
					"fee-pool genesis invariant violated: consumer %d pool %s holds %s but has no shares for that denom",
					consumerId, poolAddr.String(), coin.String(),
				))
			}
		}
	}

	k.SetParams(ctx, genState.Params)
	infractionParams := types.DefaultInfractionParameters()
	if genState.InfractionParameters != nil {
		infractionParams = *genState.InfractionParameters
	}
	if err := types.ValidateInfractionParamsAgainst(infractionParams, genState.Params.TrustingPeriodFraction); err != nil {
		panic(fmt.Errorf("infraction parameters incompatible with genesis trusting period fraction: %w", err))
	}
	k.SetInfractionParams(ctx, infractionParams)

	// Import downtime-detection state: pending slashes, the previous downtime
	// params (only when present -- absent means the params have never
	// changed), and per-(consumer, distribution-time) epoch share records.
	for _, pending := range genState.PendingDowntimeSlashes {
		key := collections.Join(pending.ConsumerId, pending.ProviderConsAddr)
		if err := k.PendingDowntimeSlashes.Set(ctx, key, pending); err != nil {
			panic(fmt.Errorf("init: set pending downtime slash for consumer %d: %w", pending.ConsumerId, err))
		}
	}
	if genState.PreviousDowntimeParams != nil {
		if err := k.PreviousDowntimeParams.Set(ctx, *genState.PreviousDowntimeParams); err != nil {
			panic(fmt.Errorf("init: set previous downtime params: %w", err))
		}
	}
	for _, rec := range genState.EpochShareRecords {
		key := collections.Join(rec.ConsumerId, rec.DistributedAtUnixNano)
		if err := k.EpochShareRecords.Set(ctx, key, rec.Share); err != nil {
			panic(fmt.Errorf("init: set epoch share record for consumer %d: %w", rec.ConsumerId, err))
		}
	}

	// Import withheld-fee escrow, the last-punished-window bookkeeping, and
	// this epoch's downtime marks, so a state-export restart preserves them
	// exactly as the other downtime-detection state above is preserved.
	for _, rec := range genState.WithheldFeeRecords {
		key := collections.Join(rec.ConsumerId, rec.ProviderConsAddr)
		if err := k.WithheldFeeRecords.Set(ctx, key, rec); err != nil {
			panic(fmt.Errorf("init: set withheld fee record for consumer %d: %w", rec.ConsumerId, err))
		}
	}
	for _, e := range genState.LastPunishedWindowEnds {
		key := collections.Join(e.ConsumerId, e.ProviderConsAddr)
		if err := k.LastPunishedWindowEnds.Set(ctx, key, e.WindowEndHeight); err != nil {
			panic(fmt.Errorf("init: set last punished window end for consumer %d: %w", e.ConsumerId, err))
		}
	}
	for _, e := range genState.EpochDowntimeEntries {
		key := collections.Join(e.ConsumerId, e.ProviderConsAddr)
		if err := k.EpochDowntime.Set(ctx, key, true); err != nil {
			panic(fmt.Errorf("init: set epoch downtime mark for consumer %d: %w", e.ConsumerId, err))
		}
	}

	return k.InitGenesisValUpdates(ctx)
}

// InitGenesisValUpdates returns the genesis validator set updates
// for the provider module from the full staking bonded validator set.
func (k Keeper) InitGenesisValUpdates(ctx sdk.Context) []abci.ValidatorUpdate {
	// get the staking validator set
	valSet, err := k.stakingKeeper.GetBondedValidatorsByPower(ctx)
	if err != nil {
		panic(fmt.Errorf("retrieving validator set: %w", err))
	}

	consensusValSet := make([]types.ConsensusValidator, len(valSet))
	for i, val := range valSet {
		consensusVal, err := k.CreateProviderConsensusValidator(ctx, val)
		if err != nil {
			panic(fmt.Errorf("creating provider consensus validator: %w", err))
		}
		consensusValSet[i] = consensusVal
	}

	err = k.SetLastProviderConsensusValSet(ctx, consensusValSet)
	if err != nil {
		panic(fmt.Errorf("setting the provider consensus validator set: %w", err))
	}

	valUpdates := make([]abci.ValidatorUpdate, len(consensusValSet))
	for i, val := range consensusValSet {
		valUpdates[i] = abci.ValidatorUpdate{
			PubKey: *val.PublicKey,
			Power:  val.Power,
		}
	}
	return valUpdates
}

// ExportGenesis returns the VAAS provider module's exported genesis
//
// Per-consumer state is exported for every consumer id (across all phases
// including DELETED) so that state-export software upgrades preserve the
// audit trail that the keeper itself maintains (see DeleteConsumerChain in
// consumer_lifecycle.go for the policy of keeping owner/metadata/init_params
// past deletion).
//
// Spawn-time queue, removal-time queue, equivocation-evidence-min-height,
// and per-consumer debt are NOT exported because they are derivable from
// the per-consumer fields above and / or other module state at InitGenesis.
//
// The liveness clock (last-ack time and the highest-sent / highest-acked VSC
// ids) IS exported per consumer and restored at InitGenesis, so a state-export
// restart preserves each consumer's grace window and resync counters rather
// than resetting them.
//
// Downtime-detection state (PendingDowntimeSlashes, PreviousDowntimeParams,
// EpochShareRecords, WithheldFeeRecords, LastPunishedWindowEnds,
// EpochDowntimeEntries) IS also exported so a state-export restart preserves
// slashes awaiting their challenge window, the grace window for evidence
// computed under superseded downtime params, the fee-share history used to
// price downtime slashes, fee escrow pending a challenge window, the
// last-punished-window bookkeeping that rejects overlapping evidence, and
// this epoch's downtime marks.
//
// The pause-expiration queue (ConsumerPauseExpirationTime /
// PauseExpirationTimeToConsumerIds) is NOT exported directly, following the
// same pattern as the spawn-time and removal-time queues: each PAUSED
// consumer's pause_expiration_time is carried on its ConsumerState and the
// queue bucket is re-derived from it at InitGenesis (see the PAUSED case
// there).
func (k Keeper) ExportGenesis(ctx sdk.Context) *types.GenesisState {
	allConsumerIds := k.GetAllConsumerIds(ctx)

	consumerStates := make([]types.ConsumerState, 0, len(allConsumerIds))
	for _, consumerId := range allConsumerIds {
		phase := k.GetConsumerPhase(ctx, consumerId)

		chainId, err := k.GetConsumerChainId(ctx, consumerId)
		if err != nil {
			panic(fmt.Errorf("export: failed to read chain id for consumer %d: %w", consumerId, err))
		}

		cs := types.ConsumerState{
			ConsumerId:           consumerId,
			ChainId:              chainId,
			Phase:                phase,
			PendingValsetChanges: k.GetPendingVSCPackets(ctx, consumerId),
		}

		if owner, err := k.GetConsumerOwnerAddress(ctx, consumerId); err == nil {
			cs.OwnerAddress = owner
		} else if !errors.Is(err, collections.ErrNotFound) {
			panic(fmt.Errorf("export: failed to read owner for consumer %d: %w", consumerId, err))
		}

		if md, err := k.GetConsumerMetadata(ctx, consumerId); err == nil {
			cs.Metadata = &md
		} else if !errors.Is(err, collections.ErrNotFound) {
			panic(fmt.Errorf("export: failed to read metadata for consumer %d: %w", consumerId, err))
		}

		if ip, err := k.GetConsumerInitializationParameters(ctx, consumerId); err == nil {
			cs.InitParams = &ip
		} else if !errors.Is(err, collections.ErrNotFound) {
			panic(fmt.Errorf("export: failed to read init params for consumer %d: %w", consumerId, err))
		}

		if clientId, ok := k.GetConsumerClientId(ctx, consumerId); ok {
			cs.ClientId = clientId
		}
		if gen, ok := k.GetConsumerGenesis(ctx, consumerId); ok {
			cs.ConsumerGenesis = gen
		}

		if rt, err := k.GetConsumerRemovalTime(ctx, consumerId); err == nil {
			rtCopy := rt // copy to avoid aliasing the loop-local variable
			cs.RemovalTime = &rtCopy
		} else if !errors.Is(err, collections.ErrNotFound) {
			panic(fmt.Errorf("export: failed to read removal time for consumer %d: %w", consumerId, err))
		}

		if pet, err := k.GetConsumerPauseExpirationTime(ctx, consumerId); err == nil {
			petCopy := pet // copy to avoid aliasing the loop-local variable
			cs.PauseExpirationTime = &petCopy
		} else if !errors.Is(err, collections.ErrNotFound) {
			panic(fmt.Errorf("export: failed to read pause expiration time for consumer %d: %w", consumerId, err))
		}

		// Liveness clock: export the last-ack time only when actually recorded
		// (GetConsumerLastAckTime falls back to block time when absent, which we
		// must not persist), and the resync counters only when non-zero (zero is
		// their absent default).
		hasAck, err := k.ConsumerLastAckTime.Has(ctx, consumerId)
		if err != nil {
			panic(fmt.Errorf("export: failed to check last ack time for consumer %d: %w", consumerId, err))
		}
		if hasAck {
			ackCopy := k.GetConsumerLastAckTime(ctx, consumerId)
			cs.LastAckTime = &ackCopy
		}
		if v := k.GetConsumerHighestSentVscId(ctx, consumerId); v != 0 {
			cs.HighestSentVscId = v
		}
		if v := k.GetConsumerHighestAckedVscId(ctx, consumerId); v != 0 {
			cs.HighestAckedVscId = v
		}

		consumerStates = append(consumerStates, cs)
	}

	consumerAddrsToPrune := []types.ConsumerAddrsToPrune{}
	for _, consumerId := range allConsumerIds {
		consumerAddrsToPrune = append(consumerAddrsToPrune,
			k.GetAllConsumerAddrsToPrune(ctx, consumerId)...)
	}

	overrides := []types.ConsumerFeesPerBlockOverride{}
	if err := k.ConsumerFeesPerBlockOverride.Walk(ctx, nil, func(consumerId uint64, amt math.Int) (bool, error) {
		overrides = append(overrides, types.ConsumerFeesPerBlockOverride{
			ConsumerId: consumerId,
			Amount:     amt.String(),
		})
		return false, nil
	}); err != nil {
		panic(fmt.Errorf("export: walk fees-per-block overrides: %w", err))
	}

	// Export share records and accumulate per-(consumer, denom) totals in
	// a single pass for the sanity check below.
	feePoolShares, recomputedTotals := k.exportFeePoolShares(ctx)

	// Sanity check: compare stored ConsumerFeePoolTotalShares against the
	// totals we just recomputed. Divergence is logged but not blocking —
	// export should never fail on a sanity check.
	k.checkFeePoolTotalsConsistency(ctx, recomputedTotals)

	pendingDowntimeSlashes := k.exportPendingDowntimeSlashes(ctx)

	var previousDowntimeParams *types.PreviousDowntimeParams
	if p, err := k.PreviousDowntimeParams.Get(ctx); err == nil {
		pCopy := p
		previousDowntimeParams = &pCopy
	} else if !errors.Is(err, collections.ErrNotFound) {
		panic(fmt.Errorf("export: failed to read previous downtime params: %w", err))
	}

	epochShareRecords := k.exportEpochShareRecords(ctx)

	withheldFeeRecords := k.exportWithheldFeeRecords(ctx)
	lastPunishedWindowEnds := k.exportLastPunishedWindowEnds(ctx)
	epochDowntimeEntries := k.exportEpochDowntimeEntries(ctx)

	// Only export infraction params if they have actually been set (e.g. a
	// keeper built directly via setters in a test, without ever running
	// InitGenesis, may never have called SetInfractionParams). A real chain
	// always has them set by InitGenesis.
	var infractionParams *types.InfractionParameters
	if has, err := k.InfractionParams.Has(ctx); err == nil && has {
		ip := k.GetInfractionParams(ctx)
		infractionParams = &ip
	} else if err != nil {
		panic(fmt.Errorf("export: failed to check infraction params: %w", err))
	}

	// TODO (PERMISSIONLESS)
	genState := types.NewGenesisState(
		k.GetValidatorSetUpdateId(ctx),
		k.GetAllValsetUpdateBlockHeights(ctx),
		consumerStates,
		k.GetParams(ctx),
		k.GetAllValidatorConsumerPubKeys(ctx, nil),
		k.GetAllValidatorsByConsumerAddr(ctx, nil),
		consumerAddrsToPrune,
		overrides,
		feePoolShares,
	)
	genState.PendingDowntimeSlashes = pendingDowntimeSlashes
	genState.PreviousDowntimeParams = previousDowntimeParams
	genState.EpochShareRecords = epochShareRecords
	genState.InfractionParameters = infractionParams
	genState.WithheldFeeRecords = withheldFeeRecords
	genState.LastPunishedWindowEnds = lastPunishedWindowEnds
	genState.EpochDowntimeEntries = epochDowntimeEntries
	return genState
}

// exportWithheldFeeRecords walks WithheldFeeRecords into the flat list
// carried by genesis.
func (k Keeper) exportWithheldFeeRecords(ctx sdk.Context) []types.WithheldFeeRecord {
	records := []types.WithheldFeeRecord{}
	iter, err := k.WithheldFeeRecords.Iterate(ctx, nil)
	if err != nil {
		panic(fmt.Errorf("export: failed to iterate withheld fee records: %w", err))
	}
	defer iter.Close()
	for ; iter.Valid(); iter.Next() {
		val, err := iter.Value()
		if err != nil {
			panic(fmt.Errorf("export: failed to read withheld fee record: %w", err))
		}
		records = append(records, val)
	}
	return records
}

// exportLastPunishedWindowEnds walks LastPunishedWindowEnds into the flat
// list carried by genesis.
func (k Keeper) exportLastPunishedWindowEnds(ctx sdk.Context) []types.LastPunishedWindowEnd {
	entries := []types.LastPunishedWindowEnd{}
	iter, err := k.LastPunishedWindowEnds.Iterate(ctx, nil)
	if err != nil {
		panic(fmt.Errorf("export: failed to iterate last punished window ends: %w", err))
	}
	defer iter.Close()
	for ; iter.Valid(); iter.Next() {
		key, err := iter.Key()
		if err != nil {
			panic(fmt.Errorf("export: failed to read last punished window end key: %w", err))
		}
		val, err := iter.Value()
		if err != nil {
			panic(fmt.Errorf("export: failed to read last punished window end value: %w", err))
		}
		entries = append(entries, types.LastPunishedWindowEnd{
			ConsumerId:       key.K1(),
			ProviderConsAddr: key.K2(),
			WindowEndHeight:  val,
		})
	}
	return entries
}

// exportEpochDowntimeEntries walks EpochDowntime into the flat list carried
// by genesis. Presence in the collection always means "down" (the keeper
// only ever calls Set(ctx, key, true)), so the bool value itself is not
// carried.
func (k Keeper) exportEpochDowntimeEntries(ctx sdk.Context) []types.EpochDowntimeEntry {
	entries := []types.EpochDowntimeEntry{}
	iter, err := k.EpochDowntime.Iterate(ctx, nil)
	if err != nil {
		panic(fmt.Errorf("export: failed to iterate epoch downtime marks: %w", err))
	}
	defer iter.Close()
	for ; iter.Valid(); iter.Next() {
		key, err := iter.Key()
		if err != nil {
			panic(fmt.Errorf("export: failed to read epoch downtime key: %w", err))
		}
		entries = append(entries, types.EpochDowntimeEntry{
			ConsumerId:       key.K1(),
			ProviderConsAddr: key.K2(),
		})
	}
	return entries
}

// exportPendingDowntimeSlashes walks PendingDowntimeSlashes into the flat
// list carried by genesis.
func (k Keeper) exportPendingDowntimeSlashes(ctx sdk.Context) []types.PendingDowntimeSlash {
	pending := []types.PendingDowntimeSlash{}
	iter, err := k.PendingDowntimeSlashes.Iterate(ctx, nil)
	if err != nil {
		panic(fmt.Errorf("export: failed to iterate pending downtime slashes: %w", err))
	}
	defer iter.Close()
	for ; iter.Valid(); iter.Next() {
		val, err := iter.Value()
		if err != nil {
			panic(fmt.Errorf("export: failed to read pending downtime slash: %w", err))
		}
		pending = append(pending, val)
	}
	return pending
}

// exportEpochShareRecords walks EpochShareRecords into the flat list carried
// by genesis.
func (k Keeper) exportEpochShareRecords(ctx sdk.Context) []types.EpochShareRecord {
	records := []types.EpochShareRecord{}
	iter, err := k.EpochShareRecords.Iterate(ctx, nil)
	if err != nil {
		panic(fmt.Errorf("export: failed to iterate epoch share records: %w", err))
	}
	defer iter.Close()
	for ; iter.Valid(); iter.Next() {
		key, err := iter.Key()
		if err != nil {
			panic(fmt.Errorf("export: failed to read epoch share record key: %w", err))
		}
		val, err := iter.Value()
		if err != nil {
			panic(fmt.Errorf("export: failed to read epoch share record value: %w", err))
		}
		records = append(records, types.EpochShareRecord{
			ConsumerId:            key.K1(),
			DistributedAtUnixNano: key.K2(),
			Share:                 val,
		})
	}
	return records
}

// exportFeePoolShares walks ConsumerFeePoolShares once, emitting both the
// flat list for genesis export and a per-(consumer, denom) recomputed-totals
// map for the export-time sanity check.
func (k Keeper) exportFeePoolShares(
	ctx sdk.Context,
) ([]types.ConsumerFeePoolShare, map[uint64]map[string]math.Int) {
	shares := []types.ConsumerFeePoolShare{}
	totals := map[uint64]map[string]math.Int{}
	iter, err := k.ConsumerFeePoolShares.Iterate(ctx, nil)
	if err != nil {
		k.Logger(ctx).Error("fee-pool shares: iterate failed at export", "error", err.Error())
		return shares, totals
	}
	defer iter.Close()
	for ; iter.Valid(); iter.Next() {
		key, err := iter.Key()
		if err != nil {
			k.Logger(ctx).Error("fee-pool shares: key read failed at export", "error", err.Error())
			return shares, totals
		}
		val, err := iter.Value()
		if err != nil {
			k.Logger(ctx).Error("fee-pool shares: value read failed at export", "error", err.Error())
			return shares, totals
		}
		shares = append(shares, types.ConsumerFeePoolShare{
			ConsumerId: key.K1(),
			Depositor:  key.K3().String(),
			Denom:      key.K2(),
			Shares:     val,
		})
		if _, ok := totals[key.K1()]; !ok {
			totals[key.K1()] = map[string]math.Int{}
		}
		if cur, ok := totals[key.K1()][key.K2()]; ok {
			totals[key.K1()][key.K2()] = cur.Add(val)
		} else {
			totals[key.K1()][key.K2()] = val
		}
	}
	return shares, totals
}

// checkFeePoolTotalsConsistency compares stored ConsumerFeePoolTotalShares
// against a recomputed total map and logs both directions of mismatch.
func (k Keeper) checkFeePoolTotalsConsistency(
	ctx sdk.Context, recomputed map[uint64]map[string]math.Int,
) {
	seen := map[uint64]map[string]bool{}
	totIter, err := k.ConsumerFeePoolTotalShares.Iterate(ctx, nil)
	if err != nil {
		k.Logger(ctx).Error("fee-pool totals: iterate failed at export", "error", err.Error())
		return
	}
	defer totIter.Close()
	for ; totIter.Valid(); totIter.Next() {
		key, err := totIter.Key()
		if err != nil {
			k.Logger(ctx).Error("fee-pool totals: key read failed at export", "error", err.Error())
			return
		}
		val, err := totIter.Value()
		if err != nil {
			k.Logger(ctx).Error("fee-pool totals: value read failed at export", "error", err.Error())
			return
		}
		consumerId, denom := key.K1(), key.K2()
		if _, ok := seen[consumerId]; !ok {
			seen[consumerId] = map[string]bool{}
		}
		seen[consumerId][denom] = true

		expected, ok := recomputed[consumerId][denom]
		if !ok || !expected.Equal(val) {
			k.Logger(ctx).Error(
				"fee-pool total_shares: stored != recomputed at export",
				"consumer_id", consumerId, "denom", denom,
				"stored", val.String(),
			)
		}
	}
	// Reverse direction: recomputed total exists but no stored total.
	for consumerId, byDenom := range recomputed {
		for denom := range byDenom {
			if !seen[consumerId][denom] {
				k.Logger(ctx).Error(
					"fee-pool total_shares: shares exist without stored total at export",
					"consumer_id", consumerId, "denom", denom,
				)
			}
		}
	}
}
