package app_test

import (
	"encoding/json"
	"testing"
	"time"

	dbm "github.com/cosmos/cosmos-db"
	"github.com/stretchr/testify/require"

	"cosmossdk.io/log"
	sdkmath "cosmossdk.io/math"

	abci "github.com/cometbft/cometbft/abci/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	cmttypes "github.com/cometbft/cometbft/types"

	"github.com/cosmos/cosmos-sdk/baseapp"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	simtestutil "github.com/cosmos/cosmos-sdk/testutil/sims"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	slashingtypes "github.com/cosmos/cosmos-sdk/x/slashing/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	app "github.com/allinbits/vaas/app/provider"
)

// TestProviderPunishesNativeEquivocation is the C1 regression test: it proves
// that the x/evidence module is wired into the provider app end to end, so a
// CometBFT-reported DuplicateVoteEvidence against a provider validator is
// slashed, jailed, and tombstoned.
//
// This is the strongest feasible in-process test: it drives the real
// provider app through ABCI FinalizeBlock with a Misbehavior entry, exactly as
// CometBFT delivers a double-sign. baseapp threads that Misbehavior into the
// block's CometInfo, the evidence module's BeginBlocker (now registered in the
// begin-block ordering) reads it, and punishes the validator via the slashing
// keeper. A full multi-node double-sign is only reachable in the Docker e2e;
// this exercises every in-process link of the wired path.
func TestProviderPunishesNativeEquivocation(t *testing.T) {
	const (
		numVals = 3
		chainID = "provider-test-1"
	)

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

	// Pull the bond denom out of the default staking genesis.
	var stakingGen stakingtypes.GenesisState
	appCodec.MustUnmarshalJSON(genesis[stakingtypes.ModuleName], &stakingGen)
	bondDenom := stakingGen.Params.BondDenom

	var (
		validators   []stakingtypes.Validator
		delegations  []stakingtypes.Delegation
		signingInfos []slashingtypes.SigningInfo
		consAddrs    []sdk.ConsAddress
	)

	for i := 0; i < numVals; i++ {
		privVal := cmttypes.NewMockPV()
		pubKey, err := privVal.GetPubKey()
		require.NoError(t, err)

		sdkPub, err := cryptocodec.FromCmtPubKeyInterface(pubKey)
		require.NoError(t, err)
		pkAny, err := codectypes.NewAnyWithValue(sdkPub)
		require.NoError(t, err)

		valAddr := sdk.ValAddress(pubKey.Address())
		consAddr := sdk.ConsAddress(pubKey.Address())
		consAddrs = append(consAddrs, consAddr)

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

	// bank: fund the bonded pool with the staked tokens.
	bondedPoolCoins := sdk.NewCoins(sdk.NewCoin(bondDenom, bondAmt.MulRaw(numVals)))
	balances := []banktypes.Balance{{
		Address: authtypes.NewModuleAddress(stakingtypes.BondedPoolName).String(),
		Coins:   bondedPoolCoins,
	}}
	bankGen := banktypes.NewGenesisState(
		banktypes.DefaultGenesisState().Params, balances, bondedPoolCoins, nil, nil,
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
	// back and referenced as the (past) infraction height.
	infractionTime := time.Now().UTC()
	_, err = providerApp.FinalizeBlock(&abci.RequestFinalizeBlock{
		Height: providerApp.LastBlockHeight() + 1,
		Time:   infractionTime,
	})
	require.NoError(t, err)
	_, err = providerApp.Commit()
	require.NoError(t, err)
	infractionHeight := providerApp.LastBlockHeight()

	// Sanity: the target validator is bonded, not jailed, not tombstoned.
	target := consAddrs[0]
	ctx := providerApp.NewContextLegacy(true, cmtproto.Header{Height: providerApp.LastBlockHeight()})
	preVal, err := providerApp.StakingKeeper.GetValidatorByConsAddr(ctx, target)
	require.NoError(t, err)
	require.False(t, preVal.IsJailed())
	require.False(t, providerApp.SlashingKeeper.IsTombstoned(ctx, target))
	preTokens := preVal.Tokens

	// Deliver a DuplicateVote misbehaviour for the target validator through the
	// same ABCI path CometBFT uses. The evidence BeginBlocker must consume it.
	_, err = providerApp.FinalizeBlock(&abci.RequestFinalizeBlock{
		Height: providerApp.LastBlockHeight() + 1,
		Time:   infractionTime.Add(time.Second),
		Misbehavior: []abci.Misbehavior{{
			Type:             abci.MisbehaviorType_DUPLICATE_VOTE,
			Validator:        abci.Validator{Address: target.Bytes(), Power: 10},
			Height:           infractionHeight,
			Time:             infractionTime,
			TotalVotingPower: 10 * numVals,
		}},
	})
	require.NoError(t, err)
	_, err = providerApp.Commit()
	require.NoError(t, err)

	// The wired evidence path must have slashed, jailed, and tombstoned it.
	ctx = providerApp.NewContextLegacy(true, cmtproto.Header{Height: providerApp.LastBlockHeight()})
	postVal, err := providerApp.StakingKeeper.GetValidatorByConsAddr(ctx, target)
	require.NoError(t, err)
	require.True(t, postVal.IsJailed(), "double-signing validator must be jailed")
	require.True(t, providerApp.SlashingKeeper.IsTombstoned(ctx, target), "double-signing validator must be tombstoned")
	require.True(t, postVal.Tokens.LT(preTokens),
		"double-signing validator must be slashed: pre=%s post=%s", preTokens, postVal.Tokens)

	// A non-offending validator must be untouched.
	other, err := providerApp.StakingKeeper.GetValidatorByConsAddr(ctx, consAddrs[1])
	require.NoError(t, err)
	require.False(t, other.IsJailed(), "non-offending validator must not be jailed")
	require.False(t, providerApp.SlashingKeeper.IsTombstoned(ctx, consAddrs[1]))
}
