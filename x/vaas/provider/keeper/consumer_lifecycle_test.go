package keeper_test

import (
	"testing"

	"cosmossdk.io/math"
	testkeeper "github.com/allinbits/vaas/testutil/keeper"
	providertypes "github.com/allinbits/vaas/x/vaas/provider/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"
)

// TestDeleteConsumerChain_FeesPerBlockOverrideCleanup verifies that
// DeleteConsumerChain unconditionally clears any per-consumer override entry
// and is a no-op when none is set.
func TestDeleteConsumerChain_FeesPerBlockOverrideCleanup(t *testing.T) {
	// A zero-value math.Int (IsNil()) in seedOverride means "no override is set".
	cases := []struct {
		name         string
		seedOverride math.Int
	}{
		{
			name:         "override present is cleared",
			seedOverride: math.NewInt(2500),
		},
		{
			name: "no override is a no-op",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			params := testkeeper.NewInMemKeeperParams(t)
			k, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, params)
			defer ctrl.Finish()

			consumerId := k.FetchAndIncrementConsumerId(ctx)
			k.SetConsumerPhase(ctx, consumerId, providertypes.CONSUMER_PHASE_STOPPED)
			// DeleteConsumerClientId panics on a missing client mapping, so seed one.
			k.SetConsumerClientId(ctx, consumerId, "07-tendermint-0")

			if !tc.seedOverride.IsNil() {
				require.NoError(t, k.ConsumerFeesPerBlockOverride.Set(ctx, consumerId, tc.seedOverride))
			}

			sdkCtx := sdk.UnwrapSDKContext(ctx)
			require.NoError(t, k.DeleteConsumerChain(sdkCtx, consumerId))

			has, err := k.ConsumerFeesPerBlockOverride.Has(ctx, consumerId)
			require.NoError(t, err)
			require.False(t, has)
		})
	}
}
