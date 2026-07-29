package keeper

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/allinbits/vaas/x/vaas/provider/types"
	vaastypes "github.com/allinbits/vaas/x/vaas/types"

	tmtypes "github.com/cometbft/cometbft/types"

	ibcclienttypes "github.com/cosmos/ibc-go/v10/modules/core/02-client/types"
	ibctmtypes "github.com/cosmos/ibc-go/v10/modules/light-clients/07-tendermint"

	errorsmod "cosmossdk.io/errors"
	"cosmossdk.io/math"

	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	slashingtypes "github.com/cosmos/cosmos-sdk/x/slashing/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

//
// Double Voting section
//

// HandleConsumerDoubleVoting verifies a double voting evidence for a given a consumer id
// and a public key and, if successful, executes the slashing, jailing, and tombstoning of the malicious validator.
func (k Keeper) HandleConsumerDoubleVoting(
	ctx sdk.Context,
	consumerId uint64,
	evidence *tmtypes.DuplicateVoteEvidence,
	pubkey cryptotypes.PubKey,
) error {
	// check that the evidence is for an ICS consumer chain
	if k.GetConsumerPhase(ctx, consumerId) != types.CONSUMER_PHASE_LAUNCHED {
		return errorsmod.Wrapf(
			vaastypes.ErrInvalidDoubleVotingEvidence,
			"consumer chain %d is not launched",
			consumerId,
		)
	}

	// check that the evidence is not too old
	minHeight := k.GetEquivocationEvidenceMinHeight(ctx, consumerId)
	if uint64(evidence.VoteA.Height) < minHeight {
		return errorsmod.Wrapf(
			vaastypes.ErrInvalidDoubleVotingEvidence,
			"evidence for consumer chain %d is too old - evidence height (%d), min (%d)",
			consumerId,
			evidence.VoteA.Height,
			minHeight,
		)
	}

	// get the chainId of this consumer chain to verify the double-voting evidence
	chainId, err := k.GetConsumerChainId(ctx, consumerId)
	if err != nil {
		return err
	}

	// verifies the double voting evidence using the consumer chain public key
	if err = k.VerifyDoubleVotingEvidence(*evidence, chainId, pubkey); err != nil {
		return err
	}

	// get the validator's consensus address on the provider
	providerAddr := k.GetProviderAddrFromConsumerAddr(
		ctx,
		consumerId,
		types.NewConsumerConsAddress(sdk.ConsAddress(evidence.VoteA.ValidatorAddress.Bytes())),
	)

	// get infraction parameters
	infractionParams := k.GetInfractionParams(ctx)

	alreadyTombstoned, err := k.punishEquivocation(ctx, providerAddr, infractionParams.DoubleSign)
	if err != nil {
		return err
	}

	k.Logger(ctx).Info(
		"confirmed equivocation",
		"consumerId", consumerId,
		"chainId", chainId,
		"byzantine validator address", providerAddr.String(),
		"already_tombstoned", alreadyTombstoned,
	)

	return nil
}

// VerifyDoubleVotingEvidence verifies a double voting evidence
// for a given chain id and a validator public key
func (k Keeper) VerifyDoubleVotingEvidence(
	evidence tmtypes.DuplicateVoteEvidence,
	chainId string,
	pubkey cryptotypes.PubKey,
) error {
	if pubkey == nil {
		return fmt.Errorf("validator public key cannot be empty")
	}

	// check that the validator address in the evidence is derived from the provided public key
	if !bytes.Equal(pubkey.Address(), evidence.VoteA.ValidatorAddress) {
		return errorsmod.Wrapf(
			vaastypes.ErrInvalidDoubleVotingEvidence,
			"public key %s doesn't correspond to the validator address %s in double vote evidence",
			pubkey.String(), evidence.VoteA.ValidatorAddress.String(),
		)
	}

	// Note the age of the evidence isn't checked.

	// height/round/type must be the same
	if evidence.VoteA.Height != evidence.VoteB.Height ||
		evidence.VoteA.Round != evidence.VoteB.Round ||
		evidence.VoteA.Type != evidence.VoteB.Type {
		return errorsmod.Wrapf(
			vaastypes.ErrInvalidDoubleVotingEvidence,
			"height/round/type are not the same: %d/%d/%v vs %d/%d/%v",
			evidence.VoteA.Height, evidence.VoteA.Round, evidence.VoteA.Type,
			evidence.VoteB.Height, evidence.VoteB.Round, evidence.VoteB.Type)
	}

	// Addresses must be the same
	if !bytes.Equal(evidence.VoteA.ValidatorAddress, evidence.VoteB.ValidatorAddress) {
		return errorsmod.Wrapf(
			vaastypes.ErrInvalidDoubleVotingEvidence,
			"validator addresses do not match: %X vs %X",
			evidence.VoteA.ValidatorAddress,
			evidence.VoteB.ValidatorAddress,
		)
	}

	// BlockIDs must be different
	if evidence.VoteA.BlockID.Equals(evidence.VoteB.BlockID) {
		return errorsmod.Wrapf(
			vaastypes.ErrInvalidDoubleVotingEvidence,
			"block IDs are the same (%v) - not a real duplicate vote",
			evidence.VoteA.BlockID,
		)
	}

	va := evidence.VoteA.ToProto()
	vb := evidence.VoteB.ToProto()

	// signatures must be valid
	if !pubkey.VerifySignature(tmtypes.VoteSignBytes(chainId, va), evidence.VoteA.Signature) {
		return fmt.Errorf("verifying VoteA: %w", tmtypes.ErrVoteInvalidSignature)
	}
	if !pubkey.VerifySignature(tmtypes.VoteSignBytes(chainId, vb), evidence.VoteB.Signature) {
		return fmt.Errorf("verifying VoteB: %w", tmtypes.ErrVoteInvalidSignature)
	}

	return nil
}

//
// Light Client Attack (IBC misbehavior) section
//

// HandleConsumerMisbehaviour verifies an IBC light-client misbehaviour for a
// consumer chain and, when it is a confirmed equivocation light-client attack,
// punishes the byzantine validators identically to vote-level double signing:
// the validators that signed both conflicting headers are slashed, jailed, and
// tombstoned at the DoubleSign infraction severity through the same
// punishEquivocation primitive HandleConsumerDoubleVoting uses. The two paths
// differ only in how the evidence is verified (two votes vs two headers), not
// in how the equivocation is punished.
//
// When the attack is cryptographically confirmed but no validator can be
// punished, the response escalates to the chain level rather than silently
// doing nothing; see slashOrEscalateLightClientAttack.
//
// Returns the provider consensus addresses that were punished (empty when the
// attack was escalated to the chain level).
func (k Keeper) HandleConsumerMisbehaviour(ctx sdk.Context, consumerId uint64, misbehaviour ibctmtypes.Misbehaviour) ([]types.ProviderConsAddress, error) {
	logger := k.Logger(ctx)

	// Check that the misbehaviour is valid and that the client consensus states at trusted heights are within trusting period
	if err := k.CheckMisbehaviour(ctx, consumerId, misbehaviour); err != nil {
		logger.Info("misbehaviour rejected", "error", err.Error())

		return nil, err
	}

	// Since the misbehaviour packet was received within the trusting period
	// w.r.t to the trusted consensus states the infraction age
	// isn't too old. see ibc-go/modules/light-clients/07-tendermint/types/misbehaviour_handle.go

	// Get Byzantine validators from the conflicting headers
	byzantineValidators, err := k.GetByzantineValidators(ctx, misbehaviour)
	if err != nil {
		return nil, err
	}

	return k.slashOrEscalateLightClientAttack(ctx, consumerId, byzantineValidators)
}

// slashOrEscalateLightClientAttack applies the punishment policy for a
// confirmed light-client attack whose byzantine set has already been extracted
// from the conflicting headers. Each identified validator is slashed, jailed,
// and tombstoned at the DoubleSign severity via punishEquivocation -- the same
// primitive that punishes vote-level double signing -- so a single validator
// that cannot be slashed (e.g. already unbonded) does not prevent punishing the
// rest of the coalition, and re-submitted evidence for an already-tombstoned
// validator is idempotent.
//
// When no validator could be punished the attack is escalated to the chain
// level via escalateUnpunishableLightClientAttack: an amnesia attack has no
// identifiable byzantine set by construction (GetByzantineValidators returns
// none), and other conflicts may leave only unbonded signers, yet a confirmed
// attack must never be a silent no-op. Returns the punished provider consensus
// addresses (empty when the attack was escalated).
func (k Keeper) slashOrEscalateLightClientAttack(
	ctx sdk.Context,
	consumerId uint64,
	byzantineValidators []*tmtypes.Validator,
) ([]types.ProviderConsAddress, error) {
	logger := k.Logger(ctx)
	infractionParams := k.GetInfractionParams(ctx)

	punished := make([]types.ProviderConsAddress, 0, len(byzantineValidators))
	for _, v := range byzantineValidators {
		providerAddr := k.GetProviderAddrFromConsumerAddr(
			ctx,
			consumerId,
			types.NewConsumerConsAddress(sdk.ConsAddress(v.Address.Bytes())),
		)

		alreadyTombstoned, err := k.punishEquivocation(ctx, providerAddr, infractionParams.DoubleSign)
		if err != nil {
			logger.Error(
				"failed to punish byzantine validator for light client attack",
				"consumerId", consumerId,
				"providerAddr", providerAddr.String(),
				"error", err.Error(),
			)
			continue
		}

		punished = append(punished, providerAddr)
		logger.Info(
			"punished byzantine validator for light client attack",
			"consumerId", consumerId,
			"providerAddr", providerAddr.String(),
			"already_tombstoned", alreadyTombstoned,
		)
	}

	if len(punished) == 0 {
		if err := k.escalateUnpunishableLightClientAttack(ctx, consumerId); err != nil {
			return nil, err
		}
		return punished, nil
	}

	logger.Info(
		"confirmed equivocation light client attack",
		"consumerId", consumerId,
		"byzantine_validators", punished,
	)

	return punished, nil
}

// escalateUnpunishableLightClientAttack is the chain-level response to a
// confirmed light-client attack for which no validator could be held
// accountable. The consumer proved able to produce conflicting valid headers
// yet no individual can be punished, so its consensus is treated as compromised
// and the chain is stopped and scheduled for removal through the standard
// lifecycle path (StopAndPrepareForConsumerRemoval). Only a launched consumer
// is escalated: once it has already left the launched phase (e.g. a
// re-submission of the same evidence after the first escalation) this is a
// no-op, so the removal is not scheduled twice.
func (k Keeper) escalateUnpunishableLightClientAttack(ctx sdk.Context, consumerId uint64) error {
	logger := k.Logger(ctx)

	if phase := k.GetConsumerPhase(ctx, consumerId); phase != types.CONSUMER_PHASE_LAUNCHED {
		logger.Info(
			"confirmed light client attack with no punishable validators; consumer already stopping",
			"consumerId", consumerId,
			"phase", phase.String(),
		)
		return nil
	}

	if err := k.StopAndPrepareForConsumerRemoval(ctx, consumerId); err != nil {
		return fmt.Errorf("escalating unpunishable light client attack for consumer %d: %w", consumerId, err)
	}

	logger.Info(
		"confirmed light client attack with no punishable validators; stopped consumer and scheduled removal",
		"consumerId", consumerId,
	)
	return nil
}

// GetByzantineValidators returns the validators that signed both headers.
// If the misbehavior is an equivocation light client attack, then these
// validators are the Byzantine validators.
func (k Keeper) GetByzantineValidators(ctx sdk.Context, misbehaviour ibctmtypes.Misbehaviour) (validators []*tmtypes.Validator, err error) {
	// construct the trusted and conflicted light blocks
	lightBlock1, err := headerToLightBlock(*misbehaviour.Header1)
	if err != nil {
		return validators, err
	}
	lightBlock2, err := headerToLightBlock(*misbehaviour.Header2)
	if err != nil {
		return validators, err
	}

	// Check if the misbehaviour corresponds to an Amnesia attack,
	// meaning that the conflicting headers have both valid state transitions
	// and different commit rounds. In this case, we return no validators as
	// we can't identify the byzantine validators.
	//
	// Note that we cannot differentiate which of the headers is trusted or malicious,
	if !headersStateTransitionsAreConflicting(*lightBlock1.Header, *lightBlock2.Header) && lightBlock1.Commit.Round != lightBlock2.Commit.Round {
		return validators, nil
	}

	// compare the signatures of the headers
	// and return the intersection of validators who signed both

	// create a map with the validators' address that signed header1
	header1Signers := map[string]int{}
	for idx, sign := range lightBlock1.Commit.Signatures {
		if sign.BlockIDFlag == tmtypes.BlockIDFlagAbsent {
			continue
		}
		header1Signers[sign.ValidatorAddress.String()] = idx
	}

	// iterate over the header2 signers and check if they signed header1
	for sigIdxHeader2, sign := range lightBlock2.Commit.Signatures {
		if sign.BlockIDFlag == tmtypes.BlockIDFlagAbsent {
			continue
		}
		if sigIdxHeader1, ok := header1Signers[sign.ValidatorAddress.String()]; ok {
			if err := verifyLightBlockCommitSig(*lightBlock1, sigIdxHeader1); err != nil {
				return nil, err
			}

			if err := verifyLightBlockCommitSig(*lightBlock2, sigIdxHeader2); err != nil {
				return nil, err
			}

			_, val := lightBlock1.ValidatorSet.GetByAddress(sign.ValidatorAddress)
			validators = append(validators, val)
		}
	}

	return validators, nil
}

// headerToLightBlock returns a CometBFT light block from the given IBC header
func headerToLightBlock(h ibctmtypes.Header) (*tmtypes.LightBlock, error) {
	sh, err := tmtypes.SignedHeaderFromProto(h.SignedHeader)
	if err != nil {
		return nil, err
	}

	vs, err := tmtypes.ValidatorSetFromProto(h.ValidatorSet)
	if err != nil {
		return nil, err
	}

	return &tmtypes.LightBlock{
		SignedHeader: sh,
		ValidatorSet: vs,
	}, nil
}

// CheckMisbehaviour checks that headers in the given misbehaviour forms
// a valid light client attack from an ICS consumer chain and that the light client isn't expired
func (k Keeper) CheckMisbehaviour(ctx sdk.Context, consumerId uint64, misbehaviour ibctmtypes.Misbehaviour) error {
	chainId := misbehaviour.Header1.Header.ChainID

	consumerChainId, err := k.GetConsumerChainId(ctx, consumerId)
	if err != nil {
		return err
	} else if consumerChainId != chainId {
		return fmt.Errorf("incorrect misbehaviour for a different chain id (%s) than that of the consumer chain %s (consumerId: %d)",
			chainId,
			consumerChainId,
			consumerId)
	}

	// check that the misbehaviour is for an ICS consumer chain
	clientId, found := k.GetConsumerClientId(ctx, consumerId)
	if !found {
		return fmt.Errorf("incorrect misbehaviour with conflicting headers from a non-existent consumer chain (consumerId: %d)", consumerId)
	} else if misbehaviour.ClientId != clientId {
		return fmt.Errorf("incorrect misbehaviour: expected client ID for consumer chain with id %d is %s got %s",
			consumerId,
			clientId,
			misbehaviour.ClientId,
		)
	}

	// Check that the headers are at the same height to ensure that
	// the misbehaviour is for a light client attack and not a time violation,
	// see ibc-go/modules/light-clients/07-tendermint/types/misbehaviour_handle.go
	if !misbehaviour.Header1.GetHeight().EQ(misbehaviour.Header2.GetHeight()) {
		return errorsmod.Wrap(ibcclienttypes.ErrInvalidMisbehaviour, "headers are not at same height")
	}

	// Check that the evidence is not too old
	minHeight := k.GetEquivocationEvidenceMinHeight(ctx, consumerId)
	evidenceHeight := misbehaviour.Header1.GetHeight().GetRevisionHeight()
	// Note that the revision number is not relevant for checking the age of evidence
	// as it's already part of the chain ID and the minimum height is mapped to chain IDs
	if evidenceHeight < minHeight {
		return errorsmod.Wrapf(
			vaastypes.ErrInvalidDoubleVotingEvidence,
			"evidence for consumer chain %d is too old - evidence height (%d), min (%d)",
			consumerId,
			evidenceHeight,
			minHeight,
		)
	}

	lightClientModule := ibctmtypes.NewLightClientModule(k.cdc, k.clientKeeper.GetStoreProvider())

	// CheckForMisbehaviour verifies that the headers have different blockID hashes
	ok := lightClientModule.CheckForMisbehaviour(ctx, clientId, &misbehaviour)
	if !ok {
		return errorsmod.Wrapf(ibcclienttypes.ErrInvalidMisbehaviour, "invalid misbehaviour for client-id: %s", misbehaviour.ClientId)
	}

	// VerifyClientMessage calls verifyMisbehaviour which verifies that the headers in the misbehaviour
	// are valid against their respective trusted consensus states and that at least a TrustLevel of the validator set signed their commit,
	// see checkMisbehaviourHeader in ibc-go/blob/v7.3.0/modules/light-clients/07-tendermint/misbehaviour_handle.go#L126
	if err := lightClientModule.VerifyClientMessage(ctx, clientId, &misbehaviour); err != nil {
		return err
	}

	return nil
}

// Check if the given block headers have conflicting state transitions.
// Note that this method was copied from ConflictingHeaderIsInvalid in CometBFT,
// see https://github.com/cometbft/cometbft/blob/v0.34.27/types/evidence.go#L285
func headersStateTransitionsAreConflicting(h1, h2 tmtypes.Header) bool {
	return !bytes.Equal(h1.ValidatorsHash, h2.ValidatorsHash) ||
		!bytes.Equal(h1.NextValidatorsHash, h2.NextValidatorsHash) ||
		!bytes.Equal(h1.ConsensusHash, h2.ConsensusHash) ||
		!bytes.Equal(h1.AppHash, h2.AppHash) ||
		!bytes.Equal(h1.LastResultsHash, h2.LastResultsHash)
}

func verifyLightBlockCommitSig(lightBlock tmtypes.LightBlock, sigIdx int) error {
	// get signature
	sig := lightBlock.Commit.Signatures[sigIdx]

	// get validator
	idx, val := lightBlock.ValidatorSet.GetByAddress(sig.ValidatorAddress)
	if idx == -1 {
		return fmt.Errorf("incorrect signature: validator address %s isn't part of the validator set", sig.ValidatorAddress.String())
	}

	// verify validator pubkey corresponds to signature validator address
	if !bytes.Equal(val.PubKey.Address(), sig.ValidatorAddress) {
		return fmt.Errorf("validator public key doesn't correspond to signature validator address: %s!= %s", val.PubKey.Address(), sig.ValidatorAddress)
	}

	// validate signature
	voteSignBytes := lightBlock.Commit.VoteSignBytes(lightBlock.ChainID, int32(sigIdx))
	if !val.PubKey.VerifySignature(voteSignBytes, sig.Signature) {
		return fmt.Errorf("wrong signature (#%d): %X", sigIdx, sig.Signature)
	}

	return nil
}

//
// Punish Validator section
//

// punishEquivocation slashes, jails, and tombstones the validator identified by
// providerAddr at the given (DoubleSign) infraction severity. It is the shared
// punishment primitive behind both equivocation paths -- vote-level double
// signing (HandleConsumerDoubleVoting) and header-level light-client attacks
// (HandleConsumerMisbehaviour) -- which differ only in how they verify the
// evidence, not in how the equivocation is punished.
//
// Re-submitted evidence for an already-tombstoned validator is idempotent: the
// validator is not punished twice and no error is returned. The returned bool
// reports whether the validator was already tombstoned.
func (k Keeper) punishEquivocation(ctx sdk.Context, providerAddr types.ProviderConsAddress, params *types.SlashJailParameters) (bool, error) {
	if err := k.SlashValidator(ctx, providerAddr, params, stakingtypes.Infraction_INFRACTION_DOUBLE_SIGN); err != nil {
		if errors.Is(err, slashingtypes.ErrValidatorTombstoned) {
			return true, nil
		}
		return false, err
	}

	if err := k.JailAndTombstoneValidator(ctx, providerAddr, params); err != nil {
		if errors.Is(err, slashingtypes.ErrValidatorTombstoned) {
			return true, nil
		}
		return false, err
	}

	return false, nil
}

// JailAndTombstoneValidator jails and tombstones the validator with the given provider consensus address
func (k Keeper) JailAndTombstoneValidator(ctx sdk.Context, providerAddr types.ProviderConsAddress, jailingParams *types.SlashJailParameters) error {
	validator, err := k.stakingKeeper.GetValidatorByConsAddr(ctx, providerAddr.ToSdkConsAddr())
	if err != nil && errors.Is(err, stakingtypes.ErrNoValidatorFound) {
		return errorsmod.Wrapf(slashingtypes.ErrNoValidatorForAddress, "provider consensus address: %s", providerAddr.String())
	} else if err != nil {
		return errorsmod.Wrapf(slashingtypes.ErrBadValidatorAddr, "unknown error looking for provider consensus address: %s", providerAddr.String())
	}

	if validator.IsUnbonded() {
		return errorsmod.Wrapf(stakingtypes.ErrNoUnbondingDelegation, "validator is unbonded. provider consensus address: %s", providerAddr.String())
	}

	if k.slashingKeeper.IsTombstoned(ctx, providerAddr.ToSdkConsAddr()) {
		return errorsmod.Wrapf(slashingtypes.ErrValidatorTombstoned, "provider consensus address: %s", providerAddr.String())
	}

	// jail validator if not already
	if !validator.IsJailed() {
		err := k.stakingKeeper.Jail(ctx, providerAddr.ToSdkConsAddr())
		if err != nil {
			return err
		}
	}

	jailEndTime := ctx.BlockTime().Add(jailingParams.JailDuration)
	err = k.slashingKeeper.JailUntil(ctx, providerAddr.ToSdkConsAddr(), jailEndTime)
	if err != nil {
		return fmt.Errorf("fail to set jail duration for validator: %s: %s", providerAddr.String(), err)
	}

	if jailingParams.Tombstone {
		// Tombstone the validator so that we cannot slash the validator more than once
		// Note that we cannot simply use the fact that a validator is jailed to avoid slashing more than once
		// because then a validator could i) perform an equivocation, ii) get jailed (e.g., through downtime)
		// and in such a case the validator would not get slashed when we call `SlashValidator`.
		if err = k.slashingKeeper.Tombstone(ctx, providerAddr.ToSdkConsAddr()); err != nil {
			return fmt.Errorf("fail to tombstone validator: %s: %s", providerAddr.String(), err)
		}
	}

	return nil
}

// ComputePowerToSlash computes the power to be slashed based on the tokens in non-matured `undelegations` and
// `redelegations`, as well as the current `power` of the validator.
// Note that this method does not perform any slashing.
func (k Keeper) ComputePowerToSlash(ctx sdk.Context, validator stakingtypes.Validator, undelegations []stakingtypes.UnbondingDelegation,
	redelegations []stakingtypes.Redelegation, power int64, powerReduction math.Int,
) int64 {
	// compute the total numbers of tokens currently being undelegated
	undelegationsInTokens := math.NewInt(0)

	// Note that we use a **cached** context to avoid any actual slashing of undelegations or redelegations.
	cachedCtx, _ := ctx.CacheContext()
	for _, u := range undelegations {
		// v50: errors are ignored
		amountSlashed, _ := k.stakingKeeper.SlashUnbondingDelegation(cachedCtx, u, 0, math.LegacyNewDec(1))
		undelegationsInTokens = undelegationsInTokens.Add(amountSlashed)
	}

	// compute the total numbers of tokens currently being redelegated
	redelegationsInTokens := math.NewInt(0)
	for _, r := range redelegations {
		// v50 errors are ignored
		amountSlashed, _ := k.stakingKeeper.SlashRedelegation(cachedCtx, validator, r, 0, math.LegacyNewDec(1))
		redelegationsInTokens = redelegationsInTokens.Add(amountSlashed)
	}

	// The power we pass to staking's keeper `Slash` method is the current power of the validator together with the total
	// power of all the currently undelegated and redelegated tokens (see docs/docs/adrs/adr-013-equivocation-slashing.md).
	undelegationsAndRedelegationsInPower := sdk.TokensToConsensusPower(
		undelegationsInTokens.Add(redelegationsInTokens), powerReduction)

	return power + undelegationsAndRedelegationsInPower
}

// slashableStake looks up the validator behind providerAddr and computes the
// consensus power -- and its token equivalent -- that would be slashed for
// it, folding in the power currently tied up in non-matured undelegations
// and redelegations (see ComputePowerToSlash). Returns
// ErrNoValidatorForAddress / ErrBadValidatorAddr / ErrNoUnbondingDelegation /
// ErrValidatorTombstoned under the same conditions SlashValidator has always
// rejected under; callers that only care about rejecting those conditions
// (rather than computing a fraction from tokens) can keep using
// SlashValidator directly.
func (k Keeper) slashableStake(ctx sdk.Context, providerAddr types.ProviderConsAddress) (
	totalPower int64,
	totalTokens math.Int,
	consAddr sdk.ConsAddress,
	err error,
) {
	validator, err := k.stakingKeeper.GetValidatorByConsAddr(ctx, providerAddr.ToSdkConsAddr())
	if err != nil && errors.Is(err, stakingtypes.ErrNoValidatorFound) {
		return 0, totalTokens, consAddr, errorsmod.Wrapf(slashingtypes.ErrNoValidatorForAddress, "provider consensus address: %s", providerAddr.String())
	} else if err != nil {
		return 0, totalTokens, consAddr, errorsmod.Wrapf(slashingtypes.ErrBadValidatorAddr, "unknown error looking for provider consensus address: %s", providerAddr.String())
	}

	if validator.IsUnbonded() {
		return 0, totalTokens, consAddr, errorsmod.Wrapf(stakingtypes.ErrNoUnbondingDelegation, "validator is unbonded. provider consensus address: %s", providerAddr.String())
	}

	if k.slashingKeeper.IsTombstoned(ctx, providerAddr.ToSdkConsAddr()) {
		return 0, totalTokens, consAddr, errorsmod.Wrapf(slashingtypes.ErrValidatorTombstoned, "validator is tombstoned. provider consensus address: %s", providerAddr.String())
	}

	valAddr, err := k.ValidatorAddressCodec().StringToBytes(validator.GetOperator())
	if err != nil {
		return 0, totalTokens, consAddr, err
	}

	undelegations, err := k.stakingKeeper.GetUnbondingDelegationsFromValidator(ctx, valAddr)
	if err != nil {
		return 0, totalTokens, consAddr, err
	}
	redelegations, err := k.stakingKeeper.GetRedelegationsFromSrcValidator(ctx, valAddr)
	if err != nil {
		return 0, totalTokens, consAddr, err
	}
	lastPower, err := k.stakingKeeper.GetLastValidatorPower(ctx, valAddr)
	if err != nil {
		return 0, totalTokens, consAddr, err
	}

	powerReduction := k.stakingKeeper.PowerReduction(ctx)
	totalPower = k.ComputePowerToSlash(ctx, validator, undelegations, redelegations, lastPower, powerReduction)
	totalTokens = sdk.TokensFromConsensusPower(totalPower, powerReduction)

	consAddr, err = validator.GetConsAddr()
	if err != nil {
		return totalPower, totalTokens, consAddr, err
	}

	return totalPower, totalTokens, consAddr, nil
}

// SlashValidator slashes validator with given provider Address
func (k Keeper) SlashValidator(ctx sdk.Context, providerAddr types.ProviderConsAddress, slashingParams *types.SlashJailParameters, infraction stakingtypes.Infraction) error {
	totalPower, _, consAddr, err := k.slashableStake(ctx, providerAddr)
	if err != nil {
		return err
	}

	_, err = k.stakingKeeper.SlashWithInfractionReason(ctx, consAddr, 0, totalPower, slashingParams.SlashFraction, infraction)
	return err
}
