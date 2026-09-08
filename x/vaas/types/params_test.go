package types_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"cosmossdk.io/math"

	"github.com/allinbits/vaas/x/vaas/types"
)

func TestDefaultConsumerParamsDowntimeWindowDefaults(t *testing.T) {
	p := types.DefaultConsumerParams()
	require.NoError(t, p.Validate())
	require.Equal(t, types.DefaultSignedBlocksWindow, p.SignedBlocksWindow)
	require.False(t, p.MinSignedPerWindow.IsNil())
	require.Equal(t, math.LegacyMustNewDecFromStr(types.DefaultMinSignedPerWindow), p.MinSignedPerWindow)
}

func TestNewConsumerParamsAppliesDowntimeWindowDefaults(t *testing.T) {
	p := types.NewConsumerParams(true, types.DefaultVAASTimeoutPeriod, 1000, types.DefaultConsumerUnbondingPeriod, types.DefaultSafeModeThreshold)
	require.False(t, p.MinSignedPerWindow.IsNil())
	require.Equal(t, types.DefaultSignedBlocksWindow, p.SignedBlocksWindow)
	require.Equal(t, math.LegacyMustNewDecFromStr(types.DefaultMinSignedPerWindow), p.MinSignedPerWindow)
}

func TestConsumerParamsValidateSignedBlocksWindow(t *testing.T) {
	bad := types.DefaultConsumerParams()
	bad.SignedBlocksWindow = 0
	require.Error(t, bad.Validate())

	bad = types.DefaultConsumerParams()
	bad.MinSignedPerWindow = math.LegacyNewDec(2)
	require.Error(t, bad.Validate())

	bad = types.DefaultConsumerParams()
	bad.MinSignedPerWindow = math.LegacyZeroDec()
	require.Error(t, bad.Validate())
}
