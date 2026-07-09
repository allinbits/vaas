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
	k.SetInfractionParams(ctx, types.DefaultInfractionParameters())

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
	// export should never fail on a sanity check (spec section "Genesis
	// export/import").
	k.checkFeePoolTotalsConsistency(ctx, recomputedTotals)

	// TODO (PERMISSIONLESS)
	return types.NewGenesisState(
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
