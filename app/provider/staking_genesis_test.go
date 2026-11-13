package app_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	dbm "github.com/cosmos/cosmos-db"

	"cosmossdk.io/log"

	simtestutil "github.com/cosmos/cosmos-sdk/testutil/sims"
	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	app "github.com/allinbits/vaas/app/provider"
)

// TestDefaultGenesisPricesKeyRotationFeeInBondDenom runs the default genesis
// under the bond denom `init --default-denom` sets and checks the staking key
// rotation fee follows it.
func TestDefaultGenesisPricesKeyRotationFeeInBondDenom(t *testing.T) {
	defaultBondDenom := sdk.DefaultBondDenom
	t.Cleanup(func() { sdk.DefaultBondDenom = defaultBondDenom })

	providerApp := app.New(log.NewNopLogger(), dbm.NewMemDB(), nil, true, simtestutil.EmptyAppOptions{})

	for _, denom := range []string{"uatone", defaultBondDenom} {
		sdk.DefaultBondDenom = denom
		genesis := app.NewDefaultGenesisState(providerApp.AppCodec())

		var stakingGen stakingtypes.GenesisState
		providerApp.AppCodec().MustUnmarshalJSON(genesis[stakingtypes.ModuleName], &stakingGen)
		require.Equal(t, denom, stakingGen.Params.BondDenom)
		require.Equal(t, sdk.NewCoin(denom, stakingtypes.DefaultKeyRotationFee.Amount), stakingGen.Params.KeyRotationFee)
	}
}
