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

	// Load primary share records
	for _, s := range genState.ConsumerFeePoolShares {
		addr, err := sdk.AccAddressFromBech32(s.Depositor)
		if err != nil {
			panic(fmt.Errorf("invalid depositor in genesis: %w", err))
		}
		if err := k.ConsumerFeePoolShares.Set(ctx,
			collections.Join3(s.ConsumerId, addr, s.Denom), s.Shares,
		); err != nil {
			panic(err)
		}
	}

	// Rebuild totals by summing per (consumer_id, denom)
	totals := map[string]map[string]math.Int{}
	for _, s := range genState.ConsumerFeePoolShares {
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
		if err := k.FeePoolAddressToConsumerId.Set(ctx,
			k.GetConsumerFeePoolAddress(consumerId), consumerId,
		); err != nil {
			panic(err)
		}
	}

	k.SetParams(ctx, genState.Params)
	return k.InitGenesisValUpdates(ctx)
}

// InitGenesisValUpdates returns the genesis validator set updates
// for the provider module by selecting the first MaxProviderConsensusValidators
// from the staking module's validator set.
func (k Keeper) InitGenesisValUpdates(ctx sdk.Context) []abci.ValidatorUpdate {
	// get the staking validator set
	valSet, err := k.stakingKeeper.GetBondedValidatorsByPower(ctx)
	if err != nil {
		panic(fmt.Errorf("retrieving validator set: %w", err))
	}

	// restrict the set to the first MaxProviderConsensusValidators
	maxVals := k.GetMaxProviderConsensusValidators(ctx)
	if int64(len(valSet)) > maxVals {
		k.Logger(ctx).Info(fmt.Sprintf("reducing validator set from %d to %d", len(valSet), maxVals))
		valSet = valSet[:maxVals]
	}

	reducedValSet := make([]types.ConsensusValidator, len(valSet))
	for i, val := range valSet {
		consensusVal, err := k.CreateProviderConsensusValidator(ctx, val)
		if err != nil {
			k.Logger(ctx).Error(fmt.Sprintf("failed to create provider consensus validator: %v", err))
			continue
		}
		reducedValSet[i] = consensusVal
	}

	err = k.SetLastProviderConsensusValSet(ctx, reducedValSet)
	if err != nil {
		panic(fmt.Errorf("setting the provider consensus validator set: %w", err))
	}

	valUpdates := make([]abci.ValidatorUpdate, len(reducedValSet))
	for i, val := range reducedValSet {
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

	// TODO (PERMISSIONLESS)
	gs := types.NewGenesisState(
		k.GetValidatorSetUpdateId(ctx),
		k.GetAllValsetUpdateBlockHeights(ctx),
		consumerStates,
		k.GetParams(ctx),
		k.GetAllValidatorConsumerPubKeys(ctx, nil),
		k.GetAllValidatorsByConsumerAddr(ctx, nil),
		consumerAddrsToPrune,
	)

	// Export share records
	iter, err := k.ConsumerFeePoolShares.Iterate(ctx, nil)
	if err != nil {
		panic(err)
	}
	defer iter.Close()
	for ; iter.Valid(); iter.Next() {
		key, err := iter.Key()
		if err != nil {
			panic(err)
		}
		val, err := iter.Value()
		if err != nil {
			panic(err)
		}
		gs.ConsumerFeePoolShares = append(gs.ConsumerFeePoolShares, types.ConsumerFeePoolShare{
			ConsumerId: key.K1(),
			Depositor:  key.K2().String(),
			Denom:      key.K3(),
			Shares:     val,
		})
	}

	// Sanity check: rebuild totals from shares and warn on divergence
	recomputed := map[string]math.Int{}
	for _, s := range gs.ConsumerFeePoolShares {
		key := s.ConsumerId + "|" + s.Denom
		if cur, ok := recomputed[key]; ok {
			recomputed[key] = cur.Add(s.Shares)
		} else {
			recomputed[key] = s.Shares
		}
	}
	totIter, err := k.ConsumerFeePoolTotalShares.Iterate(ctx, nil)
	if err != nil {
		panic(err)
	}
	for ; totIter.Valid(); totIter.Next() {
		key, err := totIter.Key()
		if err != nil {
			panic(err)
		}
		val, err := totIter.Value()
		if err != nil {
			panic(err)
		}
		composite := key.K1() + "|" + key.K2()
		expected, ok := recomputed[composite]
		if !ok || !expected.Equal(val) {
			k.Logger(ctx).Error(
				"fee-pool total_shares mismatch at export",
				"consumer_id", key.K1(), "denom", key.K2(),
				"stored", val.String(),
			)
		}
	}
	totIter.Close()

	return gs
}
