package cmd

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestNewRootCmdCarriesStakingTxCommands builds the provider root command,
// which adds every module basic's tx commands, and checks the staking ones
// are among them: the basic ModuleBasics carries for staking must hold the
// codec those commands are built from.
func TestNewRootCmdCarriesStakingTxCommands(t *testing.T) {
	root := NewRootCmd()

	cmd, _, err := root.Find([]string{"tx", "staking"})
	require.NoError(t, err)
	require.Equal(t, "staking", cmd.Name())
}
