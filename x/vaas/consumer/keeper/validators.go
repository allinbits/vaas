package keeper

import (
	"context"
	"errors"

	abci "github.com/cometbft/cometbft/abci/types"

	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	"github.com/allinbits/vaas/x/vaas/consumer/types"
	vaastypes "github.com/allinbits/vaas/x/vaas/types"
)

// ApplyVaasValidatorChanges applies the given changes to the VAAS validators states
// and returns updates to forward to tendermint.
func (k Keeper) ApplyVaasValidatorChanges(ctx context.Context, changes []abci.ValidatorUpdate) []abci.ValidatorUpdate {
	ret := []abci.ValidatorUpdate{}
	for _, change := range changes {
		// convert TM pubkey to SDK pubkey
		pubkey, err := cryptocodec.FromCmtProtoPublicKey(change.GetPubKey())
		if err != nil {
			// An error here would indicate that the validator updates
			// received from the provider are invalid.
			panic(err)
		}
		addr := pubkey.Address()
		val, found := k.GetVaasValidator(ctx, addr)

		if found { //nolint:gocritic
			// update or delete an existing validator
			if change.Power < 1 {
				k.DeleteVaasValidator(ctx, addr)
			} else {
				val.Power = change.Power
				k.SetVaasValidator(ctx, val)
			}
		} else if 0 < change.Power {
			// create a new validator
			vaasVal, err := types.NewVaasValidator(addr, change.Power, pubkey)
			if err != nil {
				// An error here would indicate that the validator updates
				// received from the provider are invalid.
				panic(err)
			}

			k.SetVaasValidator(ctx, vaasVal)
		} else {
			// edge case: we received an update for 0 power
			// but the validator is already deleted. Do not forward
			// to tendermint.
			continue
		}

		ret = append(ret, change)
	}
	return ret
}

// GetHistoricalInfo gets the historical info at a given height
func (k Keeper) GetHistoricalInfo(goCtx context.Context, height int64) (stakingtypes.HistoricalInfo, error) {
	info, err := k.HistoricalInfos.Get(goCtx, height)
	if err != nil {
		return stakingtypes.HistoricalInfo{}, stakingtypes.ErrNoHistoricalInfo
	}
	return info, nil
}

// SetHistoricalInfo sets the historical info at a given height
func (k Keeper) SetHistoricalInfo(goCtx context.Context, height int64, hi *stakingtypes.HistoricalInfo) {
	if err := k.HistoricalInfos.Set(goCtx, height, *hi); err != nil {
		panic(err)
	}
}

// DeleteHistoricalInfo deletes the historical info at a given height
func (k Keeper) DeleteHistoricalInfo(goCtx context.Context, height int64) error {
	return k.HistoricalInfos.Remove(goCtx, height)
}

// TrackHistoricalInfo saves the latest historical-info and deletes the oldest
// heights that are below pruning height
func (k Keeper) TrackHistoricalInfo(goCtx context.Context) error {
	numHistoricalEntries := k.GetHistoricalEntries(goCtx)

	// Prune store to ensure we only have parameter-defined historical entries.
	// In most cases, this will involve removing a single historical entry.
	// In the rare scenario when the historical entries gets reduced to a lower value k'
	// from the original value k. k - k' entries must be deleted from the store.
	// Since the entries to be deleted are always in a continuous range, we can iterate
	// over the historical entries starting from the most recent version to be pruned
	// and then return at the first empty entry.
	ctx := sdk.UnwrapSDKContext(goCtx)
	for i := ctx.BlockHeight() - numHistoricalEntries; i >= 0; i-- {
		_, err := k.GetHistoricalInfo(ctx, i)
		if err != nil {
			if errors.Is(err, stakingtypes.ErrNoHistoricalInfo) {
				break
			}
			return err
		}
		if err = k.DeleteHistoricalInfo(ctx, i); err != nil {
			return err
		}
	}

	// if there is no need to persist historicalInfo, return
	if numHistoricalEntries == 0 {
		return nil
	}

	// Create HistoricalInfo struct
	lastVals := []stakingtypes.Validator{}
	for _, v := range k.GetAllVaasValidator(ctx) {
		pk, err := v.ConsPubKey()
		if err != nil {
			// This should never happen as the pubkey is assumed
			// to be stored correctly in ApplyVaasValidatorChanges.
			panic(err)
		}

		val, err := stakingtypes.NewValidator(sdk.ValAddress(pk.Address()).String(), pk, stakingtypes.Description{})
		if err != nil {
			// This should never happen as the pubkey is assumed
			// to be stored correctly in ApplyVaasValidatorChanges.
			panic(err)
		}

		// Set validator to bonded status
		val.Status = stakingtypes.Bonded
		// Compute tokens from voting power
		val.Tokens = sdk.TokensFromConsensusPower(v.Power, sdk.DefaultPowerReduction)
		lastVals = append(lastVals, val)
	}

	// Create historical info entry which sorts the validator set by voting power
	historicalEntry := stakingtypes.NewHistoricalInfo(ctx.BlockHeader(), stakingtypes.Validators{Validators: lastVals, ValidatorCodec: k.validatorAddressCodec}, sdk.DefaultPowerReduction)

	// Set latest HistoricalInfo at current height
	k.SetHistoricalInfo(ctx, ctx.BlockHeight(), &historicalEntry)
	return nil
}

// MustGetCurrentValidatorsAsABCIUpdates gets all VAAS validators converted
// to the ABCI validator update type. It panics in case of failure.
func (k Keeper) MustGetCurrentValidatorsAsABCIUpdates(ctx context.Context) []abci.ValidatorUpdate {
	vals := k.GetAllVaasValidator(ctx)
	valUpdates := make([]abci.ValidatorUpdate, 0, len(vals))
	for _, v := range vals {
		pk, err := v.ConsPubKey()
		if err != nil {
			// This should never happen as the pubkey is assumed
			// to be stored correctly in ApplyVaasValidatorChanges.
			panic(err)
		}
		tmPK, err := cryptocodec.ToCmtProtoPublicKey(pk)
		if err != nil {
			// This should never happen as the pubkey is assumed
			// to be stored correctly in ApplyVaasValidatorChanges.
			panic(err)
		}
		valUpdates = append(valUpdates, abci.ValidatorUpdate{PubKey: tmPK, Power: v.Power})
	}
	return valUpdates
}

// ApplyAndReturnValidatorSetUpdates satisfies x/genutil's StakingKeeper, the
// one staking interface the consumer keeper stands in for (the consumer runs
// no staking module): genutil's InitGenesis calls it after delivering the
// genesis transactions, when there are any. It returns no updates, since the
// consumer's validator set comes from the VAAS genesis and the provider's VSC
// packets.
func (k Keeper) ApplyAndReturnValidatorSetUpdates(context.Context) (updates []abci.ValidatorUpdate, err error) {
	return
}

// computeReplaceUpdates turns a complete target set into the updates needed to
// move the consumer's current VAAS validator set to exactly that target: every
// target entry at its absolute power, plus a power-0 removal for each current
// validator absent from the target.
//
// Identity matching uses the cmt proto public key string, which is the same
// identity used by MustGetCurrentValidatorsAsABCIUpdates and ApplyVaasValidatorChanges.
//
// A pubkey repeated in the target collapses to its last entry, as the diff
// path collapses repeats through AccumulateChanges: CometBFT rejects a
// validator update list that names one address twice, which would halt every
// consumer node at EndBlock.
func (k Keeper) computeReplaceUpdates(ctx context.Context, target []abci.ValidatorUpdate) []abci.ValidatorUpdate {
	target = vaastypes.AccumulateChanges(nil, target)
	inTarget := make(map[string]struct{}, len(target))
	for _, u := range target {
		inTarget[u.PubKey.String()] = struct{}{}
	}

	current := k.GetAllVaasValidator(ctx)
	updates := make([]abci.ValidatorUpdate, 0, len(target)+len(current))
	updates = append(updates, target...)

	for _, v := range current {
		pk, err := v.ConsPubKey()
		if err != nil {
			continue
		}
		tmPk, err := cryptocodec.ToCmtProtoPublicKey(pk)
		if err != nil {
			continue
		}
		if _, ok := inTarget[tmPk.String()]; !ok {
			updates = append(updates, abci.ValidatorUpdate{PubKey: tmPk, Power: 0})
		}
	}
	return updates
}
