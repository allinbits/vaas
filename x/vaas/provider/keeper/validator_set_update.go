package keeper

import (
	"bytes"
	"fmt"

	"github.com/allinbits/vaas/x/vaas/provider/types"
	vaastypes "github.com/allinbits/vaas/x/vaas/types"

	abci "github.com/cometbft/cometbft/abci/types"
	cryptoenc "github.com/cometbft/cometbft/crypto/encoding"
	tmtypes "github.com/cometbft/cometbft/types"

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
// of the provided `bondedValidators`, dropping any entry that would put two
// validators at the same consensus address on the consumer.
//
// Two bonded validators collide when one's consumer consensus key equals the
// other's: a validator rotated its provider consensus key onto a key another
// validator had assigned as its consumer key, or a hand-assembled genesis wired
// the two that way. Key assignment and the provider ante handler refuse to
// create such a state, but a set that does contain a duplicate is fatal on both
// sides -- the provider panics hashing it (a CometBFT validator set rejects
// duplicate addresses) and the consumer halts when its consensus engine applies
// the duplicate -- so the duplicate is removed here, at the single point where
// the set is assembled, before it is stored, hashed, or queued in a VSC packet.
// Each drop is logged at error level: the set is safe to use, but the underlying
// state is not something operators should discover from a stalled chain.
//
// Of a colliding pair the entry whose provider consensus address sorts first is
// kept. That address is unique per validator (x/staking indexes validators by
// it), so the rule is a total order and never ties, and it reads nothing but the
// two addresses, so every node keeps the same validator. It deliberately does
// not depend on the order of `bondedValidators`, which is power-ranked: a mere
// power change must not silently move the consumer slot from one validator to
// the other. Nor does it prefer the holder of an assigned key over a validator
// running its default key, which would leave a genesis-born collision between
// two assigned keys unresolved.
func (k Keeper) CreateConsumerValidators(
	ctx sdk.Context,
	consumerId uint64,
	bondedValidators []stakingtypes.Validator,
) ([]types.ConsensusValidator, error) {
	var nextValidators []types.ConsensusValidator
	indexByConsumerAddr := make(map[string]int, len(bondedValidators))
	for _, val := range bondedValidators {
		nextValidator, err := k.CreateConsumerValidator(ctx, consumerId, val)
		if err != nil {
			return nextValidators, err
		}

		consumerAddr, err := vaastypes.TMCryptoPublicKeyToConsAddr(*nextValidator.PublicKey)
		if err != nil {
			return nextValidators, fmt.Errorf("could not retrieve consumer consensus address of validator (%+v): %w",
				val, err)
		}

		if i, seen := indexByConsumerAddr[string(consumerAddr)]; seen {
			kept, dropped := nextValidators[i], nextValidator
			if bytes.Compare(nextValidator.ProviderConsAddr, kept.ProviderConsAddr) < 0 {
				kept, dropped = nextValidator, kept
				nextValidators[i] = nextValidator
			}
			k.Logger(ctx).Error("two validators share a consumer consensus address; dropping one of them",
				"consumerId", consumerId,
				"consumerConsAddr", consumerAddr.String(),
				"keptProviderConsAddr", sdk.ConsAddress(kept.ProviderConsAddr).String(),
				"droppedProviderConsAddr", sdk.ConsAddress(dropped.ProviderConsAddr).String(),
			)
			continue
		}

		indexByConsumerAddr[string(consumerAddr)] = len(nextValidators)
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

// ComputeConsumerValSetHash computes the CometBFT hash of a consumer
// validator set exactly as the consumer chain's consensus engine does: the
// stored public keys (the assigned consumer keys, i.e. what the consumer
// actually runs) and powers are assembled into a canonically ordered
// tmtypes.ValidatorSet and hashed. The result is comparable byte-for-byte
// with the NextValidatorsHash the consumer's block headers -- and therefore
// the consensus states of any honest IBC client of the consumer -- carry
// while that set is the consumer's next validator set.
//
// A zero-power entry is skipped rather than hashed: everywhere in the
// protocol (ABCI updates, DiffValidators, the consumer's
// ApplyCCValidatorChanges) zero power means the validator is not in the set,
// so the consumer's consensus engine never includes it in the hash either.
func ComputeConsumerValSetHash(validators []types.ConsensusValidator) ([]byte, error) {
	tmValidators := make([]*tmtypes.Validator, 0, len(validators))
	for _, val := range validators {
		if val.PublicKey == nil {
			return nil, fmt.Errorf("consumer validator %x has no public key", val.ProviderConsAddr)
		}
		// tmtypes.NewValidatorSet panics on negative powers; a stored consumer
		// validator always has non-negative power, so surface a violation as
		// an error instead.
		if val.Power < 0 {
			return nil, fmt.Errorf("consumer validator %x has negative power %d", val.ProviderConsAddr, val.Power)
		}
		if val.Power == 0 {
			continue
		}
		pubKey, err := cryptoenc.PubKeyFromProto(*val.PublicKey)
		if err != nil {
			return nil, fmt.Errorf("converting consumer validator %x public key: %w", val.ProviderConsAddr, err)
		}
		tmValidators = append(tmValidators, tmtypes.NewValidator(pubKey, val.Power))
	}
	return tmtypes.NewValidatorSet(tmValidators).Hash(), nil
}

// FullValSetUpdates renders a complete validator set as absolute-power updates.
// Used for snapshot VSC packets: the consumer replaces its set with these,
// deriving removals against its own current set.
func FullValSetUpdates(validators []types.ConsensusValidator) []abci.ValidatorUpdate {
	updates := make([]abci.ValidatorUpdate, 0, len(validators))
	for _, v := range validators {
		updates = append(updates, abci.ValidatorUpdate{PubKey: *v.PublicKey, Power: v.Power})
	}
	return updates
}

// ComputeConsumerNextValSet computes the consumer next validator set and returns
// the validator updates to be sent to the consumer chain.
// Every active provider validator validates every consumer, except one dropped
// for sharing a consumer consensus address with another (see
// CreateConsumerValidators).
// When isSnapshot is true, it returns the full set as absolute-power updates;
// otherwise it returns the diff against currentConsumerValSet.
func (k Keeper) ComputeConsumerNextValSet(
	ctx sdk.Context,
	bondedValidators []stakingtypes.Validator,
	consumerId uint64,
	currentConsumerValSet []types.ConsensusValidator,
	isSnapshot bool,
) ([]abci.ValidatorUpdate, error) {
	nextValidators, err := k.CreateConsumerValidators(ctx, consumerId, bondedValidators)
	if err != nil {
		return []abci.ValidatorUpdate{},
			fmt.Errorf("computing next validators, consumerId(%d): %w", consumerId, err)
	}

	if err = k.SetConsumerValSet(ctx, consumerId, nextValidators); err != nil {
		return []abci.ValidatorUpdate{},
			fmt.Errorf("setting consumer validator set, consumerId(%d): %w", consumerId, err)
	}

	if isSnapshot {
		return FullValSetUpdates(nextValidators), nil
	}
	return DiffValidators(currentConsumerValSet, nextValidators), nil
}
