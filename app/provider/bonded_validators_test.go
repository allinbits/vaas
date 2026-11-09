package app_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	abci "github.com/cometbft/cometbft/abci/types"
	cmttypes "github.com/cometbft/cometbft/types"

	dbm "github.com/cosmos/cosmos-db"

	"cosmossdk.io/log"
	sdkmath "cosmossdk.io/math"

	"github.com/cosmos/cosmos-sdk/baseapp"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	simtestutil "github.com/cosmos/cosmos-sdk/testutil/sims"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	slashingtypes "github.com/cosmos/cosmos-sdk/x/slashing/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	app "github.com/allinbits/vaas/app/provider"
)

// bondedValidator is a validator newProviderAppWithBondedValidators bonds at
// genesis.
type bondedValidator struct {
	valAddr  sdk.ValAddress
	consAddr sdk.ConsAddress
	pubKey   cryptotypes.PubKey
}

// newProviderAppWithBondedValidators builds the real provider app and runs
// its genesis with numVals bonded validators, each self-delegated through one
// shared delegator account and carrying signing info, then commits one block
// at firstBlockTime so the genesis state is persisted and readable. Every
// validator's operator account holds one consensus-key rotation fee, so a
// test can rotate a key through the staking message server.
func newProviderAppWithBondedValidators(t *testing.T, chainID string, numVals int, firstBlockTime time.Time) (*app.App, []bondedValidator) {
	t.Helper()

	providerApp := app.New(log.NewNopLogger(), dbm.NewMemDB(), nil, true, simtestutil.EmptyAppOptions{}, baseapp.SetChainID(chainID))
	appCodec := providerApp.AppCodec()

	// One shared delegator account backs every validator's self-delegation.
	delegatorPriv := cmttypes.NewMockPV()
	delegatorPub, err := delegatorPriv.GetPubKey()
	require.NoError(t, err)
	delegatorSDKPub, err := cryptocodec.FromCmtPubKeyInterface(delegatorPub)
	require.NoError(t, err)
	delegatorAcc := authtypes.NewBaseAccount(delegatorSDKPub.Address().Bytes(), delegatorSDKPub, 0, 0)

	bondAmt := sdk.TokensFromConsensusPower(10, sdk.DefaultPowerReduction)

	genesis := app.NewDefaultGenesisState(appCodec)

	// Pull the bond denom and the rotation fee out of the default staking genesis.
	var stakingGen stakingtypes.GenesisState
	appCodec.MustUnmarshalJSON(genesis[stakingtypes.ModuleName], &stakingGen)
	bondDenom := stakingGen.Params.BondDenom
	rotationFee := sdk.NewCoins(stakingGen.Params.KeyRotationFee)

	var (
		vals         []bondedValidator
		validators   []stakingtypes.Validator
		delegations  []stakingtypes.Delegation
		signingInfos []slashingtypes.SigningInfo
		balances     []banktypes.Balance
	)

	for range numVals {
		privVal := cmttypes.NewMockPV()
		pubKey, err := privVal.GetPubKey()
		require.NoError(t, err)

		sdkPub, err := cryptocodec.FromCmtPubKeyInterface(pubKey)
		require.NoError(t, err)
		pkAny, err := codectypes.NewAnyWithValue(sdkPub)
		require.NoError(t, err)

		valAddr := sdk.ValAddress(pubKey.Address())
		consAddr := sdk.ConsAddress(pubKey.Address())
		vals = append(vals, bondedValidator{valAddr: valAddr, consAddr: consAddr, pubKey: sdkPub})

		validators = append(validators, stakingtypes.Validator{
			OperatorAddress:   valAddr.String(),
			ConsensusPubkey:   pkAny,
			Jailed:            false,
			Status:            stakingtypes.Bonded,
			Tokens:            bondAmt,
			DelegatorShares:   sdkmath.LegacyOneDec(),
			Description:       stakingtypes.Description{Moniker: "val"},
			UnbondingHeight:   0,
			UnbondingTime:     time.Unix(0, 0).UTC(),
			Commission:        stakingtypes.NewCommission(sdkmath.LegacyZeroDec(), sdkmath.LegacyZeroDec(), sdkmath.LegacyZeroDec()),
			MinSelfDelegation: sdkmath.ZeroInt(),
		})
		delegations = append(delegations, stakingtypes.NewDelegation(
			delegatorAcc.GetAddress().String(), valAddr.String(), sdkmath.LegacyOneDec(),
		))
		// Signing info is what the evidence handler requires to punish; genesis
		// bonding alone does not create it, so seed it here.
		signingInfos = append(signingInfos, slashingtypes.SigningInfo{
			Address:              consAddr.String(),
			ValidatorSigningInfo: slashingtypes.NewValidatorSigningInfo(consAddr, 0, 0, time.Unix(0, 0).UTC(), false, 0),
		})
		if !rotationFee.IsZero() {
			balances = append(balances, banktypes.Balance{Address: sdk.AccAddress(valAddr).String(), Coins: rotationFee})
		}
	}

	// auth: the delegator account.
	authGen := authtypes.NewGenesisState(authtypes.DefaultParams(), []authtypes.GenesisAccount{delegatorAcc})
	genesis[authtypes.ModuleName] = appCodec.MustMarshalJSON(authGen)

	// staking: the bonded validators and their self-delegations.
	stakingGen = *stakingtypes.NewGenesisState(stakingGen.Params, validators, delegations)
	genesis[stakingtypes.ModuleName] = appCodec.MustMarshalJSON(&stakingGen)

	// slashing: a non-zero double-sign fraction plus per-validator signing info.
	var slashingGen slashingtypes.GenesisState
	appCodec.MustUnmarshalJSON(genesis[slashingtypes.ModuleName], &slashingGen)
	slashingGen.Params.SlashFractionDoubleSign = sdkmath.LegacyNewDecWithPrec(5, 2) // 5%
	slashingGen.SigningInfos = signingInfos
	genesis[slashingtypes.ModuleName] = appCodec.MustMarshalJSON(&slashingGen)

	// bank: the bonded pool holds the staked tokens, each operator its rotation
	// fee, and the supply is their sum.
	bondedPoolCoins := sdk.NewCoins(sdk.NewCoin(bondDenom, bondAmt.MulRaw(int64(numVals))))
	balances = append(balances, banktypes.Balance{
		Address: authtypes.NewModuleAddress(stakingtypes.BondedPoolName).String(),
		Coins:   bondedPoolCoins,
	})
	supply := sdk.NewCoins()
	for _, balance := range balances {
		supply = supply.Add(balance.Coins...)
	}
	bankGen := banktypes.NewGenesisState(
		banktypes.DefaultGenesisState().Params, balances, supply, nil, nil,
	)
	genesis[banktypes.ModuleName] = appCodec.MustMarshalJSON(bankGen)

	stateBytes, err := json.MarshalIndent(genesis, "", " ")
	require.NoError(t, err)

	_, err = providerApp.InitChain(&abci.RequestInitChain{
		ChainId:         chainID,
		Validators:      []abci.ValidatorUpdate{},
		ConsensusParams: simtestutil.DefaultConsensusParams,
		AppStateBytes:   stateBytes,
	})
	require.NoError(t, err)

	// Commit an empty block so the genesis state is persisted and can be read
	// back.
	_, err = providerApp.FinalizeBlock(&abci.RequestFinalizeBlock{
		Height: providerApp.LastBlockHeight() + 1,
		Time:   firstBlockTime,
	})
	require.NoError(t, err)
	_, err = providerApp.Commit()
	require.NoError(t, err)

	return providerApp, vals
}

// newConsensusKey returns a fresh consensus public key no validator holds.
func newConsensusKey(t *testing.T) cryptotypes.PubKey {
	t.Helper()
	pubKey, err := cmttypes.NewMockPV().GetPubKey()
	require.NoError(t, err)
	sdkPub, err := cryptocodec.FromCmtPubKeyInterface(pubKey)
	require.NoError(t, err)
	return sdkPub
}
