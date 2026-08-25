package keeper

import (
	"encoding/base64"
	"fmt"

	"github.com/allinbits/vaas/x/vaas/provider/types"
	vaastypes "github.com/allinbits/vaas/x/vaas/types"

	tmprotocrypto "github.com/cometbft/cometbft/proto/tendermint/crypto"

	errorsmod "cosmossdk.io/errors"

	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

// ParseConsumerKey parses the ED25519 PubKey`consumerKey` from a JSON string
// and constructs its corresponding `tmprotocrypto.PublicKey`
func (k Keeper) ParseConsumerKey(consumerKey string) (tmprotocrypto.PublicKey, error) {
	// parse consumer key as long as it's in the right format
	pkType, keyStr, err := types.ParseConsumerKeyFromJson(consumerKey)
	if err != nil {
		return tmprotocrypto.PublicKey{}, err
	}

	// Note: the correct way to decide if a key type is supported is to check the
	// consensus params. However this functionality was disabled in https://github.com/cosmos/interchain-security/pull/916
	// as a quick way to get ed25519 working, avoiding amino/proto-any marshalling issues.

	// make sure the consumer key type is supported
	// cp := ctx.ConsensusParams()
	// if cp != nil && cp.Validator != nil {
	// 	if !tmstrings.StringInSlice(pkType, cp.Validator.PubKeyTypes) {
	// 		return nil, errorsmod.Wrapf(
	// 			stakingtypes.ErrValidatorPubKeyTypeNotSupported,
	// 			"got: %s, expected one of: %s", pkType, cp.Validator.PubKeyTypes,
	// 		)
	// 	}
	// }

	// For now, only accept ed25519.
	// TODO: decide what types should be supported.
	if pkType != "/cosmos.crypto.ed25519.PubKey" {
		return tmprotocrypto.PublicKey{}, errorsmod.Wrapf(
			stakingtypes.ErrValidatorPubKeyTypeNotSupported,
			"got: %s, expected: %s", pkType, "/cosmos.crypto.ed25519.PubKey",
		)
	}

	pubKeyBytes, err := base64.StdEncoding.DecodeString(keyStr)
	if err != nil {
		return tmprotocrypto.PublicKey{}, err
	}

	consumerTMPublicKey := tmprotocrypto.PublicKey{
		Sum: &tmprotocrypto.PublicKey_Ed25519{
			Ed25519: pubKeyBytes,
		},
	}

	return consumerTMPublicKey, nil
}

// AssignConsumerKey assigns the consumerKey to the validator with providerAddr
// on the consumer chain with the given `consumerId`, if it is either registered or currently
// voted on in a ConsumerAddition governance proposal
func (k Keeper) AssignConsumerKey(
	ctx sdk.Context,
	consumerId uint64,
	validator stakingtypes.Validator,
	consumerKey tmprotocrypto.PublicKey,
) error {
	if !k.IsConsumerActive(ctx, consumerId) {
		// check that the consumer chain is either registered, initialized, or launched
		return errorsmod.Wrapf(
			types.ErrInvalidPhase,
			"cannot assign a key to a consumer chain that is not in the registered, initialized, or launched phase: %d", consumerId)
	}

	consAddrTmp, err := vaastypes.TMCryptoPublicKeyToConsAddr(consumerKey)
	if err != nil {
		return err
	}
	consumerAddr := types.NewConsumerConsAddress(consAddrTmp)

	consAddrTmp, err = validator.GetConsAddr()
	if err != nil {
		return err
	}
	providerAddr := types.NewProviderConsAddress(consAddrTmp)

	// A rotation recorded earlier in this same block claims the key just as an
	// existing validator does, but is invisible to GetValidatorByConsAddr until
	// staking's EndBlock applies it -- check the block's rotation records first.
	if claimed, err := k.pendingRotationClaims(ctx, consumerAddr.ToSdkConsAddr()); err != nil {
		return err
	} else if claimed {
		return errorsmod.Wrapf(
			types.ErrConsumerKeyInUse,
			"a consensus-key rotation in this block already claims this consumer key",
		)
	}

	if existingVal, err := k.stakingKeeper.GetValidatorByConsAddr(ctx, consumerAddr.ToSdkConsAddr()); err == nil {
		// If there is already a different validator using the consumer key to validate on the provider
		// we prevent assigning the consumer key.
		if existingVal.OperatorAddress != validator.OperatorAddress {
			return errorsmod.Wrapf(
				types.ErrConsumerKeyInUse, "a different validator already uses the consumer key",
			)
		}
		// We prevent a validator from assigning the default provider key as a consumer key
		// if it has not already assigned a different consumer key
		_, found := k.GetValidatorConsumerPubKey(ctx, consumerId, providerAddr)
		if !found {
			return errorsmod.Wrapf(
				types.ErrCannotAssignDefaultKeyAssignment,
				"a validator cannot assign the default key assignment unless its key on that consumer has already been assigned",
			)
		}
	}

	if _, found := k.GetValidatorByConsumerAddr(ctx, consumerId, consumerAddr); found {
		// This consumer key is already in use, or it is to be pruned. With this check we prevent another validator
		// from assigning the same consumer key as some other validator. Additionally, we prevent a validator from
		// reusing a consumer key that it used in the past and is now to be pruned.
		return errorsmod.Wrapf(
			types.ErrConsumerKeyInUse, "a validator has or had assigned this consumer key already",
		)
	}

	// get the previous key assigned for this validator on this consumer chain
	if oldConsumerKey, found := k.GetValidatorConsumerPubKey(ctx, consumerId, providerAddr); found {
		oldConsumerAddrTmp, err := vaastypes.TMCryptoPublicKeyToConsAddr(oldConsumerKey)
		if err != nil {
			return err
		}
		oldConsumerAddr := types.NewConsumerConsAddress(oldConsumerAddrTmp)

		// check whether the consumer chain has already launched (i.e., a client
		// to the consumer was already created). A paused consumer counts as
		// launched here: it has a client and a validator set running under the
		// old address, and it is the one phase certain to have downtime state in
		// flight, since a pause is entered by a successful downtime challenge.
		// Both the challenge lookup and the re-submission defence resolve an
		// accused consumer address through this mapping, so it has to survive
		// until pruning rather than be dropped at assignment time.
		phase := k.GetConsumerPhase(ctx, consumerId)
		if phase == types.CONSUMER_PHASE_LAUNCHED || phase == types.CONSUMER_PHASE_PAUSED {
			// mark the old consumer address as prunable once UnbondingPeriod elapses;
			// note: this state is removed on EndBlock
			unbondingPeriod, err := k.stakingKeeper.UnbondingTime(ctx)
			if err != nil {
				return err
			}
			k.AppendConsumerAddrsToPrune(
				ctx,
				consumerId,
				ctx.BlockTime().Add(unbondingPeriod),
				oldConsumerAddr,
			)
		} else {
			// if the consumer chain is not registered, then remove the mapping
			// from the old consumer address to the provider address
			k.DeleteValidatorByConsumerAddr(ctx, consumerId, oldConsumerAddr)
		}
	}

	// set the mapping from this validator's provider address to the new consumer key;
	// overwrite if already exists
	// note: this state is deleted when the validator is removed from the staking module
	k.SetValidatorConsumerPubKey(ctx, consumerId, providerAddr, consumerKey)

	// set the mapping from this validator's new consensus address on the consumer
	// to its consensus address on the provider;
	// note: this state must be deleted through the pruning mechanism
	k.SetValidatorByConsumerAddr(ctx, consumerId, consumerAddr, providerAddr)

	return nil
}

// GetProviderAddrFromConsumerAddr returns the consensus address of a validator with
// consAddr set as the consensus address on a consumer chain
func (k Keeper) GetProviderAddrFromConsumerAddr(
	ctx sdk.Context,
	consumerId uint64,
	consumerAddr types.ConsumerConsAddress,
) types.ProviderConsAddress {
	// check if this address is known only to the consumer chain
	if providerConsAddr, found := k.GetValidatorByConsumerAddr(ctx, consumerId, consumerAddr); found {
		return providerConsAddr
	}
	// If mapping from consumer -> provider addr is not found, there is no assigned key,
	// and the consumer addr is the provider addr
	return types.NewProviderConsAddress(consumerAddr.ToSdkConsAddr())
}

// PruneKeyAssignments prunes the consumer addresses no longer needed
// as they cannot be referenced in slash requests (by a correct consumer)
func (k Keeper) PruneKeyAssignments(ctx sdk.Context, consumerId uint64) {
	now := ctx.BlockTime()

	consumerAddrs := k.ConsumeConsumerAddrsToPrune(ctx, consumerId, now)
	for _, addrBz := range consumerAddrs.Addresses {
		consumerAddr := types.NewConsumerConsAddress(addrBz)
		k.DeleteValidatorByConsumerAddr(ctx, consumerId, consumerAddr)
		k.Logger(ctx).Info("consumer address was pruned",
			"consumer consumerId", consumerId,
			"consumer consensus addr", consumerAddr.String(),
		)
	}
}

// DeleteKeyAssignments deletes all the state needed for key assignments on a consumer chain
func (k Keeper) DeleteKeyAssignments(ctx sdk.Context, consumerId uint64) {
	// delete ValidatorConsumerPubKey
	for _, validatorConsumerAddr := range k.GetAllValidatorConsumerPubKeys(ctx, &consumerId) {
		providerAddr := types.NewProviderConsAddress(validatorConsumerAddr.ProviderAddr)
		k.DeleteValidatorConsumerPubKey(ctx, consumerId, providerAddr)
	}

	// delete ValidatorsByConsumerAddr
	for _, validatorConsumerAddr := range k.GetAllValidatorsByConsumerAddr(ctx, &consumerId) {
		consumerAddr := types.NewConsumerConsAddress(validatorConsumerAddr.ConsumerAddr)
		k.DeleteValidatorByConsumerAddr(ctx, consumerId, consumerAddr)
	}

	// delete ValidatorConsumerPubKey
	for _, consumerAddrsToPrune := range k.GetAllConsumerAddrsToPrune(ctx, consumerId) {
		k.DeleteConsumerAddrsToPrune(ctx, consumerId, consumerAddrsToPrune.PruneTs)
	}
}

// ValidatorConsensusKeyInUse checks if the given consensus key is already
// used by validator in a consumer chain.
// Note that this method is called when a new validator is created in the x/staking module of cosmos-sdk.
// In case it panics, the TX aborts and thus, the validator is not created. See AfterValidatorCreated hook.
func (k Keeper) ValidatorConsensusKeyInUse(ctx sdk.Context, valAddr sdk.ValAddress) bool {
	// Get the validator being added in the staking module.
	val, err := k.stakingKeeper.GetValidator(ctx, valAddr)
	if err != nil {
		// Abort TX, do NOT allow validator to be created
		panic(fmt.Errorf("error finding newly created validator in staking module: %w", err))
	}

	// Get the consensus address of the validator being added
	consensusAddr, err := val.GetConsAddr()
	if err != nil {
		// Abort TX, do NOT allow validator to be created
		panic("could not get validator cons addr ")
	}

	return k.IsConsumerConsAddrInUse(ctx, consensusAddr)
}

// IsConsumerConsAddrInUse reports whether consAddr is already assigned as some
// validator's consumer consensus key on a consumer chain that still holds key
// assignments. It backs both the validator-creation check above and the
// consensus-key-rotation check the provider ante handler runs at tx admission
// (see x/vaas/provider/ante), keeping a key already in use as a consumer key
// from becoming a second validator's provider consensus address -- which would
// put two validators at the same consensus address on that consumer.
//
// The scan deliberately spans every consumer that is not deleted rather than
// only the active ones: a paused consumer keeps all of its key assignments (only
// DeleteConsumerChain clears them) and can be resumed to LAUNCHED, so a
// collision created while it is paused would surface the moment it resumes.
func (k Keeper) IsConsumerConsAddrInUse(ctx sdk.Context, consAddr sdk.ConsAddress) bool {
	for _, consumerId := range k.nonDeletedConsumerIds(ctx) {
		if _, exist := k.GetValidatorByConsumerAddr(ctx, consumerId, types.NewConsumerConsAddress(consAddr)); exist {
			return true
		}
	}
	return false
}

// nonDeletedConsumerIds returns every consumer whose per-validator state may
// still be live, i.e. every consumer that has not been deleted. That state
// outlives the launched phase: key assignments, the stored validator set, and
// the downtime and fee bookkeeping all survive a pause (which can be resumed)
// and a stop (until removal completes), and only DeleteConsumerChain clears
// them. Used by the collision guard above and by
// MigrateStateOnConsPubKeyRotation.
func (k Keeper) nonDeletedConsumerIds(ctx sdk.Context) []uint64 {
	consumerIds := []uint64{}
	for _, consumerId := range k.GetAllConsumerIds(ctx) {
		switch k.GetConsumerPhase(ctx, consumerId) {
		case types.CONSUMER_PHASE_UNSPECIFIED, types.CONSUMER_PHASE_DELETED:
			continue
		}
		consumerIds = append(consumerIds, consumerId)
	}
	return consumerIds
}
