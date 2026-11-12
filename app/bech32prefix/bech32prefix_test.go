package bech32prefix_test

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/allinbits/vaas/app/bech32prefix"
)

func TestFromEnvDefaultsWhenUnset(t *testing.T) {
	t.Setenv(bech32prefix.EnvVar, "")
	require.NoError(t, os.Unsetenv(bech32prefix.EnvVar))

	prefix, err := bech32prefix.FromEnv()
	require.NoError(t, err)
	require.Equal(t, "cosmos", prefix)
}

func TestFromEnvReadsTheVariable(t *testing.T) {
	t.Setenv(bech32prefix.EnvVar, "shard")

	prefix, err := bech32prefix.FromEnv()
	require.NoError(t, err)
	require.Equal(t, "shard", prefix)
}

func TestFromEnvRefusesMalformedValues(t *testing.T) {
	for _, value := range []string{
		"",
		"Shard",
		"SHARD",
		"9shard",
		"sh ard",
		"sh-ard",
		"shard\n",
		"cosmos1",
		strings.Repeat("a", 74),
	} {
		t.Run(value, func(t *testing.T) {
			t.Setenv(bech32prefix.EnvVar, value)

			_, err := bech32prefix.FromEnv()
			require.ErrorContains(t, err, bech32prefix.EnvVar)
		})
	}
}

func TestValidateAcceptsWellFormedPrefixes(t *testing.T) {
	for _, prefix := range []string{"cosmos", "atone", "shard", "a", "osmo2", strings.Repeat("a", 73)} {
		require.NoError(t, bech32prefix.Validate(prefix), prefix)
	}
}
