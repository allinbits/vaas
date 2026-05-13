package keeper

import (
	"errors"
	"fmt"

	"github.com/allinbits/vaas/x/vaas/provider/types"

	abci "github.com/cometbft/cometbft/abci/types"

	"cosmossdk.io/collections"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

// InitGenesis initializes the CCV provider state.
func (k Keeper) InitGenesis(ctx sdk.Context, genState *types.GenesisState) []abci.ValidatorUpdate {
	k.SetValidatorSetUpdateId(ctx, genState.ValsetUpdateId)
	for _, v2h := range genState.ValsetUpdateIdToHeight {
		k.SetValsetUpdateBlockHeight(ctx, v2h.ValsetUpdateId, v2h.Height)
	}

	// Set initial state for each consumer chain
	for _, cs := range genState.ConsumerStates {
		chainID := cs.ChainId
		if cs.ClientId != "" {
			k.SetConsumerClientId(ctx, chainID, cs.ClientId)
		}
		k.SetConsumerPhase(ctx, chainID, cs.Phase)
		if err := k.SetConsumerGenesis(ctx, chainID, cs.ConsumerGenesis); err != nil {
			// An error here would indicate something is very wrong,
			// the ConsumerGenesis validated in ConsumerState.Validate().
			panic(fmt.Errorf("consumer chain genesis could not be persisted: %w", err))
		}
		k.AppendPendingVSCPackets(ctx, chainID, cs.PendingValsetChanges...)
	}

	// Import key assignment state
	for _, item := range genState.ValidatorConsumerPubkeys {
		providerAddr := types.NewProviderConsAddress(item.ProviderAddr)
		k.SetValidatorConsumerPubKey(ctx, item.ChainId, providerAddr, *item.ConsumerKey)
	}

	for _, item := range genState.ValidatorsByConsumerAddr {
		consumerAddr := types.NewConsumerConsAddress(item.ConsumerAddr)
		providerAddr := types.NewProviderConsAddress(item.ProviderAddr)
		k.SetValidatorByConsumerAddr(ctx, item.ChainId, consumerAddr, providerAddr)
	}

	for _, item := range genState.ConsumerAddrsToPrune {
		for _, addr := range item.ConsumerAddrs.Addresses {
			consumerAddr := types.NewConsumerConsAddress(addr)
			k.AppendConsumerAddrsToPrune(ctx, item.ChainId, item.PruneTs, consumerAddr)
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

// ExportGenesis returns the CCV provider module's exported genesis.
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
			if errors.Is(err, collections.ErrNotFound) {
				// A consumer id without a chain id is a keeper invariant
				// violation; skip this entry rather than emit a partial record.
				continue
			}
			panic(fmt.Errorf("export: failed to read chain id for consumer %s: %w", consumerId, err))
		}

		cs := types.ConsumerState{
			ChainId:              chainId,
			Phase:                phase,
			PendingValsetChanges: k.GetPendingVSCPackets(ctx, consumerId),
		}

		if owner, err := k.GetConsumerOwnerAddress(ctx, consumerId); err == nil {
			cs.OwnerAddress = owner
		} else if !errors.Is(err, collections.ErrNotFound) {
			panic(fmt.Errorf("export: failed to read owner for consumer %s: %w", consumerId, err))
		}

		if md, err := k.GetConsumerMetadata(ctx, consumerId); err == nil {
			cs.Metadata = &md
		} else if !errors.Is(err, collections.ErrNotFound) {
			panic(fmt.Errorf("export: failed to read metadata for consumer %s: %w", consumerId, err))
		}

		if ip, err := k.GetConsumerInitializationParameters(ctx, consumerId); err == nil {
			cs.InitParams = &ip
		} else if !errors.Is(err, collections.ErrNotFound) {
			panic(fmt.Errorf("export: failed to read init params for consumer %s: %w", consumerId, err))
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
			panic(fmt.Errorf("export: failed to read removal time for consumer %s: %w", consumerId, err))
		}

		consumerStates = append(consumerStates, cs)
	}

	consumerAddrsToPrune := []types.ConsumerAddrsToPrune{}
	for _, consumerId := range allConsumerIds {
		consumerAddrsToPrune = append(consumerAddrsToPrune,
			k.GetAllConsumerAddrsToPrune(ctx, consumerId)...)
	}

	// TODO (PERMISSIONLESS)
	return types.NewGenesisState(
		k.GetValidatorSetUpdateId(ctx),
		k.GetAllValsetUpdateBlockHeights(ctx),
		consumerStates,
		k.GetParams(ctx),
		k.GetAllValidatorConsumerPubKeys(ctx, nil),
		k.GetAllValidatorsByConsumerAddr(ctx, nil),
		consumerAddrsToPrune,
	)
}
