package staking

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cosmos/cosmos-sdk/x/staking/keeper"
	"github.com/cosmos/cosmos-sdk/x/staking/types"
)

// TestAppModuleKeepsTheKeeperPointer: the module runs InitGenesis and
// EndBlock on the keeper the app holds, not on a copy taken at construction,
// so hooks the app sets on the keeper afterwards fire from staking's
// EndBlock (slashing's, distribution's, and the VAAS provider's).
func TestAppModuleKeepsTheKeeperPointer(t *testing.T) {
	k := &keeper.Keeper{}
	am := NewAppModule(nil, k, nil, nil, nil)

	hooks := types.NewMultiStakingHooks(types.MultiStakingHooks{})
	k.SetHooks(hooks)

	require.Same(t, k, am.keeper)
	require.Equal(t, hooks, am.keeper.Hooks(), "hooks set after construction must be visible to the module")
}
