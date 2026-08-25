package keeper

import (
	"errors"
	"fmt"

	"github.com/allinbits/vaas/x/vaas/provider/types"

	"cosmossdk.io/collections"

	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

// MigrateStateOnConsPubKeyRotation moves the provider state a validator holds
// under its provider consensus address from its old address to its new one,
// after the validator rotates its provider consensus key (x/staking consensus
// key rotation).
//
// What moves is decided per consumer, by which address the state is read back
// under:
//
//   - State the fee path keys by the validator's live consensus address --
//     EpochDowntime and WithheldFeeRecords, both reached from val.GetConsAddr()
//     in DistributeConsumerFees -- moves for every consumer, since from the
//     next distribution run on that address is the new one.
//   - State the evidence path keys by the address an accusation resolves to --
//     the key assignment itself and the downtime acceptance bookkeeping --
//     moves only for the consumers where that resolution changes. It changes
//     exactly when the validator has an assigned consumer key there: the
//     consumer keeps validating under that key, so
//     GetProviderAddrFromConsumerAddr keeps resolving it through the reverse
//     mapping, which this migration repoints at the new address. Where the
//     validator has no assigned key the consumer sees its provider key
//     directly, so the identity itself changes and evidence about the one the
//     consumer already validated under keeps resolving to the old address:
//     moving that state would leave a re-submitted window unrecognised and a
//     queued slash unchallengeable.
//   - The consumer validator set entry is keyed by the live address as well,
//     being rebuilt from the bonded validators, and moves for the same
//     consumers as the key assignment -- but for the opposite reason. Those are
//     the consumers whose set the rotation does not change, so no snapshot
//     rebuilds it (see QueueConsPubKeyRotationSnapshots) and nothing else would
//     until the next epoch boundary. The rest have their set rebuilt under the
//     new address by the rotation snapshot, and an accusation naming the
//     pre-rotation identity finds it there (see accusedConsumerValidatorAddr).
//
// It never returns an error. It is called from
// Hooks.AfterConsensusPubKeyUpdate, which x/staking invokes in EndBlock, where
// an error would halt the chain; every failure is logged and the rest of the
// migration still runs.
func (k Keeper) MigrateStateOnConsPubKeyRotation(
	ctx sdk.Context,
	oldProviderAddr, newProviderAddr types.ProviderConsAddress,
) {
	for _, consumerId := range k.nonDeletedConsumerIds(ctx) {
		if k.migrateKeyAssignment(ctx, consumerId, oldProviderAddr, newProviderAddr) {
			k.migrateConsumerValidator(ctx, consumerId, oldProviderAddr, newProviderAddr)
			k.migrateDowntimeAcceptance(ctx, consumerId, oldProviderAddr, newProviderAddr)
		}
		k.migrateFeeExclusion(ctx, consumerId, oldProviderAddr, newProviderAddr)
	}
}

// liveProviderConsAddr resolves providerAddr to the provider consensus address
// its validator holds now: providerAddr itself, or the rotated one if
// providerAddr is an address the validator has since rotated away from. One
// x/staking lookup answers both cases -- GetValidatorByConsAddr falls back to
// the old-to-new consensus address mapping a rotation records -- so the result
// is the address the validator's live-address-keyed state stands under.
//
// providerAddr comes back unchanged when x/staking knows no validator for it,
// leaving a caller that resolves an address in order to read state under it
// to find nothing there, exactly as it would have without the resolution.
// pendingRotationClaims reports whether consAddr is the target of a
// consensus-key rotation recorded in the current block. x/staking writes a
// rotation at tx time but applies it only in its own EndBlock, so inside that
// window the new key belongs to no validator that GetValidatorByConsAddr can
// see; the block's rotation records are the only place the claim exists. Used
// by AssignConsumerKey so a consumer-key assignment delivered after the
// rotation in the same block cannot take the key and hand two validators one
// consumer consensus address when EndBlock applies the rotation.
func (k Keeper) pendingRotationClaims(ctx sdk.Context, consAddr sdk.ConsAddress) (bool, error) {
	rotations, err := k.stakingKeeper.GetBlockConsPubKeyRotationHistory(ctx)
	if err != nil {
		return false, fmt.Errorf("reading current-block consensus-key rotations: %w", err)
	}
	for _, rotation := range rotations {
		pk, ok := rotation.NewConsPubkey.GetCachedValue().(cryptotypes.PubKey)
		if !ok {
			// Not decodable as a public key: staking's own handler rejected or
			// will reject this rotation, so it claims nothing.
			continue
		}
		if consAddr.Equals(sdk.ConsAddress(pk.Address())) {
			return true, nil
		}
	}
	return false, nil
}

func (k Keeper) liveProviderConsAddr(ctx sdk.Context, providerAddr types.ProviderConsAddress) types.ProviderConsAddress {
	validator, err := k.stakingKeeper.GetValidatorByConsAddr(ctx, providerAddr.ToSdkConsAddr())
	if err != nil {
		return providerAddr
	}
	consAddr, err := validator.GetConsAddr()
	if err != nil {
		return providerAddr
	}

	return types.NewProviderConsAddress(consAddr)
}

// liveConsAddrOf answers the same question as liveProviderConsAddr for a caller
// that already holds the validator, and so needs no second x/staking lookup: the
// consensus address the validator runs now. That is the address x/slashing keys
// the validator's ValidatorSigningInfo by, since a rotation writes the entry at
// the new consensus pubkey's address and deletes the one at the old address in
// the same step x/staking repoints the validator's consensus pubkey.
//
// fallback covers a validator whose consensus pubkey cannot be read at all,
// which x/staking never stores: as in liveProviderConsAddr, the address the
// caller started from comes back, leaving it no worse off than without the
// resolution.
func liveConsAddrOf(validator stakingtypes.Validator, fallback types.ProviderConsAddress) sdk.ConsAddress {
	consAddr, err := validator.GetConsAddr()
	if err != nil {
		return fallback.ToSdkConsAddr()
	}

	return consAddr
}

// migrateKeyAssignment moves the rotating validator's key-assignment state on
// consumerId, reporting whether it had an assigned consumer key there -- i.e.
// whether the consumer's view of the validator, and with it the address the
// consumer's accusations resolve to, survives the rotation.
//
// The assigned consumer key itself does not change, so moving the state keeps
// it resolving in the VSC set computation (which looks it up by the validator's
// current provider address) and keeps cross-chain evidence attributing to the
// validator.
func (k Keeper) migrateKeyAssignment(
	ctx sdk.Context,
	consumerId uint64,
	oldProviderAddr, newProviderAddr types.ProviderConsAddress,
) bool {
	consumerKey, found := k.GetValidatorConsumerPubKey(ctx, consumerId, oldProviderAddr)
	if !found {
		return false
	}

	k.DeleteValidatorConsumerPubKey(ctx, consumerId, oldProviderAddr)
	k.SetValidatorConsumerPubKey(ctx, consumerId, newProviderAddr, consumerKey)

	// Repoint every reverse mapping consumerAddr -> providerAddr that still
	// names the old address (the current assignment plus any earlier consumer
	// keys kept resolvable for pending slashes) at the new address.
	for _, entry := range k.GetAllValidatorsByConsumerAddr(ctx, &consumerId) {
		if !sdk.ConsAddress(entry.ProviderAddr).Equals(oldProviderAddr.ToSdkConsAddr()) {
			continue
		}
		k.SetValidatorByConsumerAddr(
			ctx,
			consumerId,
			types.NewConsumerConsAddress(entry.ConsumerAddr),
			newProviderAddr,
		)
	}

	return true
}

// migrateConsumerValidator moves the rotating validator's entry in consumerId's
// stored validator set to the new provider address, preserving the entry
// itself. HandleConsumerDowntime requires the accused to be in that set, and
// the set is otherwise only rebuilt at an epoch boundary -- the rotation
// snapshot skips these consumers, whose view of the validator the rotation
// leaves unchanged -- so leaving the entry under the old address would have the
// consumer's downtime evidence rejected as naming a validator outside its set
// until the next epoch.
func (k Keeper) migrateConsumerValidator(
	ctx sdk.Context,
	consumerId uint64,
	oldProviderAddr, newProviderAddr types.ProviderConsAddress,
) {
	val, found := k.GetConsumerValidator(ctx, consumerId, oldProviderAddr)
	if !found {
		return
	}

	k.DeleteConsumerValidator(ctx, consumerId, oldProviderAddr)
	val.ProviderConsAddr = newProviderAddr.ToSdkConsAddr()
	if err := k.SetConsumerValidator(ctx, consumerId, val); err != nil {
		k.Logger(ctx).Error("cannot move consumer validator entry to the rotated provider consensus address",
			"consumerId", consumerId,
			"providerConsAddr", newProviderAddr.String(),
			"error", err,
		)
	}
}

// migrateDowntimeAcceptance moves the rotating validator's downtime acceptance
// bookkeeping on consumerId: the slashes queued behind the challenge window
// (PendingDowntimeSlashes), the windows already accepted for the pair
// (AcceptedDowntimeWindows), and the pruned acceptance floor
// (DowntimeWindowFloors).
//
// All three are read back under the address an accusation resolves to. Left
// behind, the accepted windows and the floor would stop recognising a window
// re-submitted under the new address -- the same infraction accepted twice
// means the validator is slashed twice for it -- and a queued slash would
// become unchallengeable, since HandleChallengeConsumerDowntime looks for it
// under the address it resolves the accused to.
func (k Keeper) migrateDowntimeAcceptance(
	ctx sdk.Context,
	consumerId uint64,
	oldProviderAddr, newProviderAddr types.ProviderConsAddress,
) {
	oldAddrBz := oldProviderAddr.ToSdkConsAddr().Bytes()
	newAddrBz := newProviderAddr.ToSdkConsAddr().Bytes()

	pendingKeys, pendingSlashes, err := collectPerWindowEntries(ctx, k.PendingDowntimeSlashes, consumerId, oldAddrBz)
	if err != nil {
		k.Logger(ctx).Error("cannot read the rotating validator's pending downtime slashes",
			"consumerId", consumerId, "providerConsAddr", oldProviderAddr.String(), "error", err)
	}
	for i, key := range pendingKeys {
		slash := pendingSlashes[i]
		slash.ProviderConsAddr = newAddrBz
		if err := k.PendingDowntimeSlashes.Set(ctx, collections.Join3(consumerId, newAddrBz, key.K3()), slash); err != nil {
			k.Logger(ctx).Error("cannot move pending downtime slash to the rotated provider consensus address",
				"consumerId", consumerId, "providerConsAddr", newProviderAddr.String(), "error", err)
			continue
		}
		if err := k.PendingDowntimeSlashes.Remove(ctx, key); err != nil {
			k.Logger(ctx).Error("cannot delete pending downtime slash left at the old provider consensus address",
				"consumerId", consumerId, "providerConsAddr", oldProviderAddr.String(), "error", err)
		}
	}

	acceptedKeys, acceptedWindows, err := collectPerWindowEntries(ctx, k.AcceptedDowntimeWindows, consumerId, oldAddrBz)
	if err != nil {
		k.Logger(ctx).Error("cannot read the rotating validator's accepted downtime windows",
			"consumerId", consumerId, "providerConsAddr", oldProviderAddr.String(), "error", err)
	}
	for i, key := range acceptedKeys {
		if err := k.AcceptedDowntimeWindows.Set(ctx, collections.Join3(consumerId, newAddrBz, key.K3()), acceptedWindows[i]); err != nil {
			k.Logger(ctx).Error("cannot move accepted downtime window to the rotated provider consensus address",
				"consumerId", consumerId, "providerConsAddr", newProviderAddr.String(), "error", err)
			continue
		}
		if err := k.AcceptedDowntimeWindows.Remove(ctx, key); err != nil {
			k.Logger(ctx).Error("cannot delete accepted downtime window left at the old provider consensus address",
				"consumerId", consumerId, "providerConsAddr", oldProviderAddr.String(), "error", err)
		}
	}

	k.migrateDowntimeWindowFloor(ctx, consumerId, oldAddrBz, newAddrBz)
}

// migrateDowntimeWindowFloor moves the pair's pruned acceptance floor, keeping
// the higher of the two if the new address somehow already carries one. As in
// PruneAcceptedDowntimeWindows, the floor is written before the old entry is
// deleted: were the delete to succeed and the write to fail, every window the
// floor stands for would become acceptable again.
func (k Keeper) migrateDowntimeWindowFloor(ctx sdk.Context, consumerId uint64, oldAddrBz, newAddrBz []byte) {
	oldKey := collections.Join(consumerId, oldAddrBz)

	floor, err := k.DowntimeWindowFloors.Get(ctx, oldKey)
	if errors.Is(err, collections.ErrNotFound) {
		return
	}
	if err != nil {
		k.Logger(ctx).Error("cannot read the rotating validator's downtime window floor",
			"consumerId", consumerId, "error", err)
		return
	}

	newKey := collections.Join(consumerId, newAddrBz)
	if existing, err := k.DowntimeWindowFloors.Get(ctx, newKey); err == nil && existing > floor {
		floor = existing
	}
	if err := k.DowntimeWindowFloors.Set(ctx, newKey, floor); err != nil {
		k.Logger(ctx).Error("cannot move downtime window floor to the rotated provider consensus address",
			"consumerId", consumerId, "error", err)
		return
	}
	if err := k.DowntimeWindowFloors.Remove(ctx, oldKey); err != nil {
		k.Logger(ctx).Error("cannot delete downtime window floor left at the old provider consensus address",
			"consumerId", consumerId, "error", err)
	}
}

// migrateFeeExclusion moves the rotating validator's fee-exclusion state on
// consumerId: the current epoch's downtime mark (EpochDowntime) and the fee
// share escrowed by an earlier exclusion (WithheldFeeRecords).
//
// DistributeConsumerFees reads both under the validator's live consensus
// address, so this runs for every consumer regardless of key assignment. A
// mark left behind is a mark the next distribution run does not see: the
// validator is paid for an epoch it had accepted downtime evidence in, and no
// share is escrowed for a challenge to repay. A record left behind stays
// payable (PayWithheldFees scans the consumer, and x/staking resolves a
// rotated-away consensus address to its validator) and stays sweepable at its
// own expiry, but it would sit beside any record a later exclusion writes at
// the new address, breaking the one-record-per-pair invariant recordWithheldFee
// maintains.
func (k Keeper) migrateFeeExclusion(
	ctx sdk.Context,
	consumerId uint64,
	oldProviderAddr, newProviderAddr types.ProviderConsAddress,
) {
	oldAddrBz := oldProviderAddr.ToSdkConsAddr().Bytes()
	newAddrBz := newProviderAddr.ToSdkConsAddr().Bytes()
	oldKey := collections.Join(consumerId, oldAddrBz)
	newKey := collections.Join(consumerId, newAddrBz)

	marked, err := k.EpochDowntime.Has(ctx, oldKey)
	if err != nil {
		k.Logger(ctx).Error("cannot read the rotating validator's epoch downtime mark",
			"consumerId", consumerId, "error", err)
	} else if marked {
		if err := k.EpochDowntime.Set(ctx, newKey, true); err != nil {
			k.Logger(ctx).Error("cannot move epoch downtime mark to the rotated provider consensus address",
				"consumerId", consumerId, "providerConsAddr", newProviderAddr.String(), "error", err)
		} else if err := k.EpochDowntime.Remove(ctx, oldKey); err != nil {
			k.Logger(ctx).Error("cannot delete epoch downtime mark left at the old provider consensus address",
				"consumerId", consumerId, "providerConsAddr", oldProviderAddr.String(), "error", err)
		}
	}

	record, err := k.WithheldFeeRecords.Get(ctx, oldKey)
	if errors.Is(err, collections.ErrNotFound) {
		return
	}
	if err != nil {
		k.Logger(ctx).Error("cannot read the rotating validator's withheld fee record",
			"consumerId", consumerId, "error", err)
		return
	}
	// A record already standing at the new address is left untouched and this
	// one is left where it is: both stay payable and both age out on their own
	// expiry, whereas overwriting would drop one validator's escrow.
	if _, err := k.WithheldFeeRecords.Get(ctx, newKey); err == nil {
		k.Logger(ctx).Error("the rotated provider consensus address already carries a withheld fee record; leaving the pre-rotation record in place",
			"consumerId", consumerId, "providerConsAddr", newProviderAddr.String())
		return
	}

	record.ProviderConsAddr = newAddrBz
	if err := k.WithheldFeeRecords.Set(ctx, newKey, record); err != nil {
		k.Logger(ctx).Error("cannot move withheld fee record to the rotated provider consensus address",
			"consumerId", consumerId, "providerConsAddr", newProviderAddr.String(), "error", err)
		return
	}
	if err := k.WithheldFeeRecords.Remove(ctx, oldKey); err != nil {
		k.Logger(ctx).Error("cannot delete withheld fee record left at the old provider consensus address",
			"consumerId", consumerId, "providerConsAddr", oldProviderAddr.String(), "error", err)
	}
}

// collectPerWindowEntries gathers every entry of a per-window downtime
// collection -- one keyed (consumer, provider cons addr, window-end height) --
// held for a single (consumerId, providerConsAddr) pair. The entries are read
// out before the caller writes any of them back under a different key, so the
// writes never race the iterator.
func collectPerWindowEntries[V any](
	ctx sdk.Context,
	m collections.Map[collections.Triple[uint64, []byte, int64], V],
	consumerId uint64,
	providerConsAddr []byte,
) ([]collections.Triple[uint64, []byte, int64], []V, error) {
	iter, err := m.Iterate(
		ctx, collections.NewSuperPrefixedTripleRange[uint64, []byte, int64](consumerId, providerConsAddr),
	)
	if err != nil {
		return nil, nil, err
	}
	defer iter.Close()

	var keys []collections.Triple[uint64, []byte, int64]
	var values []V
	for ; iter.Valid(); iter.Next() {
		kv, err := iter.KeyValue()
		if err != nil {
			return nil, nil, err
		}
		keys = append(keys, kv.Key)
		values = append(values, kv.Value)
	}

	return keys, values, nil
}
