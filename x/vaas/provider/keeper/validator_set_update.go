package keeper

import (
	"fmt"

	"github.com/allinbits/vaas/x/vaas/provider/types"
	vaastypes "github.com/allinbits/vaas/x/vaas/types"

	abci "github.com/cometbft/cometbft/abci/types"

	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

// DiffValidators compares the current and the next epoch's consumer validators and returns the `ValidatorUpdate` diff
// needed by CometBFT to update the validator set on a chain.
func DiffValidators(
	currentValidators []types.ConsensusValidator,
	nextValidators []types.ConsensusValidator,
) []abci.ValidatorUpdate {
	var updates []abci.ValidatorUpdate

	isCurrentValidator := make(map[string]types.ConsensusValidator, len(currentValidators))
	for _, val := range currentValidators {
		isCurrentValidator[val.PublicKey.String()] = val
	}

	isNextValidator := make(map[string]types.ConsensusValidator, len(nextValidators))
	for _, val := range nextValidators {
		isNextValidator[val.PublicKey.String()] = val
	}

	for _, currentVal := range currentValidators {
		if nextVal, found := isNextValidator[currentVal.PublicKey.String()]; !found {
			// this consumer public key does not appear in the next validators and hence we remove the validator
			// with that consumer public key by creating an update with 0 power
			updates = append(updates, abci.ValidatorUpdate{PubKey: *currentVal.PublicKey, Power: 0})
		} else if currentVal.Power != nextVal.Power {
			// validator did not modify its consumer public key but has changed its voting power, so we
			// have to create an update with the new power
			updates = append(updates, abci.ValidatorUpdate{PubKey: *nextVal.PublicKey, Power: nextVal.Power})
		}
		// else no update is needed because neither the consumer public key changed, nor the power of the validator
	}

	for _, nextVal := range nextValidators {
		if _, found := isCurrentValidator[nextVal.PublicKey.String()]; !found {
			// this consumer public key does not exist in the current validators and hence we introduce this validator
			updates = append(updates, abci.ValidatorUpdate{PubKey: *nextVal.PublicKey, Power: nextVal.Power})
		}
	}

	return updates
}

// CreateConsumerValidator creates a consumer validator for `consumerId` from the given staking `validator`
func (k Keeper) CreateConsumerValidator(ctx sdk.Context, consumerId uint64, validator stakingtypes.Validator) (types.ConsensusValidator, error) {
	valAddr, err := sdk.ValAddressFromBech32(validator.GetOperator())
	if err != nil {
		return types.ConsensusValidator{}, err
	}
	power, err := k.stakingKeeper.GetLastValidatorPower(ctx, valAddr)
	if err != nil {
		return types.ConsensusValidator{}, fmt.Errorf("could not retrieve validator's (%+v) power: %w",
			validator, err)
	}
	consAddr, err := validator.GetConsAddr()
	if err != nil {
		return types.ConsensusValidator{}, fmt.Errorf("could not retrieve validator's (%+v) consensus address: %w",
			validator, err)
	}

	consumerPublicKey, found := k.GetValidatorConsumerPubKey(ctx, consumerId, types.NewProviderConsAddress(consAddr))
	if !found {
		consumerPublicKey, err = validator.CmtConsPublicKey()
		if err != nil {
			return types.ConsensusValidator{}, fmt.Errorf("could not retrieve validator's (%+v) public key: %w", validator, err)
		}
	}

	height := ctx.BlockHeight()
	if v, found := k.GetConsumerValidator(ctx, consumerId, types.ProviderConsAddress{Address: consAddr}); found {
		// if validator was already a consumer validator, then do not update the height set the first time
		// the validator became a consumer validator
		height = v.JoinHeight
	}

	return types.ConsensusValidator{
		ProviderConsAddr: consAddr,
		Power:            power,
		PublicKey:        &consumerPublicKey,
		JoinHeight:       height,
	}, nil
}

// CreateConsumerValidators creates a consumer validator for `consumerId` from each
// of the provided `bondedValidators`.
func (k Keeper) CreateConsumerValidators(
	ctx sdk.Context,
	consumerId uint64,
	bondedValidators []stakingtypes.Validator,
) ([]types.ConsensusValidator, error) {
	var nextValidators []types.ConsensusValidator
	for _, val := range bondedValidators {
		nextValidator, err := k.CreateConsumerValidator(ctx, consumerId, val)
		if err != nil {
			return nextValidators, err
		}
		nextValidators = append(nextValidators, nextValidator)
	}

	return nextValidators, nil
}

// GetLastBondedValidators iterates the last validator powers in the staking module
// and returns the first MaxValidators many validators with the largest powers.
func (k Keeper) GetLastBondedValidators(ctx sdk.Context) ([]stakingtypes.Validator, error) {
	maxVals, err := k.stakingKeeper.MaxValidators(ctx)
	if err != nil {
		return nil, err
	}
	return vaastypes.GetLastBondedValidatorsUtil(ctx, k.stakingKeeper, maxVals)
}

// ComputeConsumerNextValSet computes the consumer next validator set and returns
// the validator updates to be sent to the consumer chain.
// Every active provider validator validates every consumer.
func (k Keeper) ComputeConsumerNextValSet(
	ctx sdk.Context,
	bondedValidators []stakingtypes.Validator,
	consumerId uint64,
	currentConsumerValSet []types.ConsensusValidator,
) ([]abci.ValidatorUpdate, error) {
	nextValidators, err := k.CreateConsumerValidators(ctx, consumerId, bondedValidators)
	if err != nil {
		return []abci.ValidatorUpdate{},
			fmt.Errorf("computing next validators, consumerId(%d): %w", consumerId, err)
	}

	err = k.SetConsumerValSet(ctx, consumerId, nextValidators)
	if err != nil {
		return []abci.ValidatorUpdate{},
			fmt.Errorf("setting consumer validator set, consumerId(%d): %w", consumerId, err)
	}

	// get the initial updates with the latest set consumer public keys
	valUpdates := DiffValidators(currentConsumerValSet, nextValidators)

	return valUpdates, nil
}
