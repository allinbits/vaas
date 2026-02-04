package types_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	consumertypes "github.com/allinbits/vaas/x/vaas/consumer/types"
)

// Tests that all singular keys, or prefixes to fully resolves keys are non duplicate byte values.
func TestNoDuplicates(t *testing.T) {
	prefixes := consumertypes.GetAllKeyPrefixes()
	seen := []byte{}

	for _, prefix := range prefixes {
		require.NotContains(t, seen, prefix, "Duplicate key prefix: %v", prefix)
		seen = append(seen, prefix)
	}
}

// Test that the value of all byte prefixes is preserved
func TestPreserveBytePrefix(t *testing.T) {
	i := 0
	require.Equal(t, byte(0), consumertypes.PortKey()[0])
	i++
	require.Equal(t, byte(2), consumertypes.UnbondingTimeKey()[0])
	i++
	require.Equal(t, byte(3), consumertypes.ProviderClientIDKey()[0])
	i++
	require.Equal(t, byte(4), consumertypes.ProviderChannelIDKey()[0])
	i++
	require.Equal(t, byte(5), consumertypes.PendingChangesKey()[0])
	i++
	require.Equal(t, byte(7), consumertypes.PreVAASKey()[0])
	i++
	require.Equal(t, byte(8), consumertypes.InitialValSetKey()[0])
	i++
	require.Equal(t, byte(11), consumertypes.HistoricalInfoKeyPrefix()[0])
	i++
	require.Equal(t, byte(13), consumertypes.HeightValsetUpdateIDKeyPrefix()[0])
	i++
	require.Equal(t, byte(16), consumertypes.CrossChainValidatorKeyPrefix()[0])
	i++
	require.Equal(t, byte(17), consumertypes.InitGenesisHeightKey()[0])
	i++
	require.Equal(t, byte(19), consumertypes.PrevStandaloneChainKey()[0])
	i++
	require.Equal(t, byte(22), consumertypes.ParametersKey()[0])
	i++
	require.Equal(t, byte(23), consumertypes.HighestValsetUpdateIDKey()[0])
	i++

	prefixes := consumertypes.GetAllKeyPrefixes()
	require.Equal(t, len(prefixes), i)
}

func TestNoPrefixOverlap(t *testing.T) {
	keys := getAllFullyDefinedKeys()

	seenPrefixes := []byte{}
	for _, key := range keys {
		require.NotContains(t, seenPrefixes, key[0], "Duplicate key prefix: %v", key[0])
		seenPrefixes = append(seenPrefixes, key[0])
	}
}

// getAllFullyDefinedKeys returns instances of byte arrays returned from fully defined key functions.
// Note we only care about checking prefixes here, so parameters into the key functions are arbitrary.
func getAllFullyDefinedKeys() [][]byte {
	return [][]byte{
		consumertypes.PortKey(),
		consumertypes.UnbondingTimeKey(),
		consumertypes.ProviderClientIDKey(),
		consumertypes.ProviderChannelIDKey(),
		consumertypes.PendingChangesKey(),
		consumertypes.HistoricalInfoKey(0),
		consumertypes.HeightValsetUpdateIDKey(0),
		consumertypes.CrossChainValidatorKey([]byte{0x05}),
		consumertypes.PreVAASKey(),
		consumertypes.InitialValSetKey(),
		consumertypes.InitGenesisHeightKey(),
		consumertypes.PrevStandaloneChainKey(),
		consumertypes.ParametersKey(),
		consumertypes.HighestValsetUpdateIDKey(),
	}
}
