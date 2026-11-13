package app

import (
	"encoding/json"

	"github.com/cosmos/cosmos-sdk/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/staking"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

// stakingModuleBasic is the staking module basic ModuleBasics carries: the
// basic of the staking module the app runs, which holds the codec its CLI
// commands need, with a default genesis that prices the consensus-key rotation
// fee in the bond denom in force when it runs (sdk.DefaultBondDenom, which
// `init --default-denom` sets first). cosmos-sdk's x/staking fixes
// stakingtypes.DefaultKeyRotationFee at package initialisation, so its own
// default genesis prices the fee in "stake" whatever the bond denom is, and a
// rotation then fails on a chain whose accounts hold none.
type stakingModuleBasic struct {
	staking.AppModuleBasic
}

// DefaultGenesis returns x/staking's default genesis with the key rotation fee
// priced in sdk.DefaultBondDenom.
func (stakingModuleBasic) DefaultGenesis(cdc codec.JSONCodec) json.RawMessage {
	genesis := stakingtypes.DefaultGenesisState()
	genesis.Params.KeyRotationFee = sdk.NewCoin(sdk.DefaultBondDenom, genesis.Params.KeyRotationFee.Amount)
	return cdc.MustMarshalJSON(genesis)
}
