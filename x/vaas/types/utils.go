package types

import (
	"sort"

	abci "github.com/cometbft/cometbft/abci/types"
	tmprotocrypto "github.com/cometbft/cometbft/proto/tendermint/crypto"

	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

func AccumulateChanges(currentChanges, newChanges []abci.ValidatorUpdate) []abci.ValidatorUpdate {
	m := make(map[string]abci.ValidatorUpdate)

	for i := range currentChanges {
		m[currentChanges[i].PubKey.String()] = currentChanges[i]
	}

	for i := range newChanges {
		m[newChanges[i].PubKey.String()] = newChanges[i]
	}

	var out []abci.ValidatorUpdate

	for _, update := range m {
		out = append(out, update)
	}

	// The list of tendermint updates should hash the same across all consensus nodes
	// that means it is necessary to sort for determinism.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Power != out[j].Power {
			return out[i].Power > out[j].Power
		}
		return out[i].PubKey.String() > out[j].PubKey.String()
	})

	return out
}

// TMCryptoPublicKeyToConsAddr converts a TM public key to an SDK public key
// and returns the associated consensus address
func TMCryptoPublicKeyToConsAddr(k tmprotocrypto.PublicKey) (sdk.ConsAddress, error) {
	sdkK, err := cryptocodec.FromCmtProtoPublicKey(k)
	if err != nil {
		return nil, err
	}
	return sdk.GetConsAddress(sdkK), nil
}

// GetLastBondedValidatorsUtil iterates the last validator powers in the staking module
// and returns the first maxVals many validators with the largest powers.
func GetLastBondedValidatorsUtil(ctx sdk.Context, stakingKeeper StakingKeeper, maxVals uint32) ([]stakingtypes.Validator, error) {
	// get the bonded validators from the staking module, sorted by power
	bondedValidators, err := stakingKeeper.GetBondedValidatorsByPower(ctx)
	if err != nil {
		return nil, err
	}

	// get the first maxVals many validators
	if uint32(len(bondedValidators)) < maxVals {
		return bondedValidators, nil // no need to truncate
	}

	bondedValidators = bondedValidators[:maxVals]

	return bondedValidators, nil
}

// BitmapIsSet reports whether bit i of bitmap is set, treating bits outside
// the bitmap as unset.
func BitmapIsSet(bitmap []byte, i int64) bool {
	byteIdx := i / 8
	if i < 0 || byteIdx >= int64(len(bitmap)) {
		return false
	}
	return bitmap[byteIdx]&(byte(1)<<uint(i%8)) != 0
}

// BitmapSet sets bit i of bitmap in place. The caller must ensure the bitmap
// is long enough to hold bit i.
func BitmapSet(bitmap []byte, i int64) {
	bitmap[i/8] |= byte(1) << uint(i%8)
}
