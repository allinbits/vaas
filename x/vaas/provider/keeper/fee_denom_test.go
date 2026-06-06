package keeper_test

import (
	"fmt"
	"testing"

	providerkeeper "github.com/allinbits/vaas/x/vaas/provider/keeper"
	"github.com/stretchr/testify/require"

	"cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"
	govkeeper "github.com/cosmos/cosmos-sdk/x/gov/keeper"
)

// NewKeeper fixes the per-block fee denom at construction. The guard exists so
// that GetFeesPerBlock's sdk.NewCoin(feeDenom, amount) can never panic at
// runtime: a bad denom is a wiring mistake that must fail loudly at startup
// rather than surface later as a malformed fee coin. For each rejected denom we
// assert both halves of that contract: the denom genuinely breaks coin
// construction, and NewKeeper refuses it up front. The guard is the first
// statement in NewKeeper, so nil dependencies are fine here; the valid-denom
// path is exercised by every other keeper test through the in-mem helper.
func TestNewKeeperRejectsInvalidFeeDenom(t *testing.T) {
	assertRejected := func(denom string) {
		require.Panicsf(t, func() {
			sdk.NewCoin(denom, math.OneInt())
		}, "sanity: %q must be an invalid coin denom for the guard to be meaningful", denom)

		defer func() {
			r := recover()
			require.NotNilf(t, r, "expected NewKeeper to panic for fee denom %q", denom)
			require.Containsf(t, fmt.Sprint(r), "fee denom", "denom %q should trip the fee-denom guard", denom)
		}()
		providerkeeper.NewKeeper(
			nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
			govkeeper.Keeper{},
			"authority",
			nil, nil,
			"fee_collector",
			denom,
		)
	}

	for _, denom := range []string{"", "1leadingdigit", "white space"} {
		assertRejected(denom)
	}
}
