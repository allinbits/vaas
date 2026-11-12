package types_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/crypto"

	"cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/allinbits/vaas/x/vaas/types"
)

// TestEvidencePacketDataCrossesBech32Prefixes pins the wire encoding of the
// accused address: the consumer encodes it under its own bech32 prefix
// configuration and the provider decodes it under another, and both must
// name the same validator. A bech32 string on the wire would fail the
// decode; raw bytes do not.
func TestEvidencePacketDataCrossesBech32Prefixes(t *testing.T) {
	addr := sdk.ConsAddress([]byte("20-byte-cons-address"))
	packet := types.NewEvidencePacketData(addr, 100, []byte{0x3F}, 8, 600, math.LegacyMustNewDecFromStr("0.5"))
	require.NoError(t, packet.Validate())

	// cosmos-sdk caches an address's bech32 string across prefix changes; the
	// test switches prefixes, so the cache is off for its duration.
	sdk.SetAddrCacheEnabled(false)
	t.Cleanup(func() { sdk.SetAddrCacheEnabled(true) })

	config := sdk.GetConfig()
	providerConsPrefix, providerConsPubPrefix := config.GetBech32ConsensusAddrPrefix(), config.GetBech32ConsensusPubPrefix()
	t.Cleanup(func() { config.SetBech32PrefixForConsensusNode(providerConsPrefix, providerConsPubPrefix) })

	// Encode as the consumer, whose prefix is not the provider's.
	config.SetBech32PrefixForConsensusNode("shardvalcons", "shardvalconspub")
	bz := packet.GetBytes()
	require.NotContains(t, string(bz), "shardvalcons",
		"the accused address must not cross the wire as a bech32 string")
	require.NotContains(t, string(bz), addr.String())

	// Decode as the provider.
	config.SetBech32PrefixForConsensusNode(providerConsPrefix, providerConsPubPrefix)
	var decoded types.EvidencePacketData
	require.NoError(t, json.Unmarshal(bz, &decoded))
	require.NoError(t, decoded.Validate())
	require.Equal(t, addr, decoded.ConsAddress(), "the provider must resolve the same validator the consumer accused")
	require.True(t, strings.HasPrefix(decoded.ConsAddress().String(), providerConsPrefix))
}

// TestEvidencePacketDataValidateRejectsBadAddresses pins the address check:
// the accused address must be exactly a CometBFT consensus address.
func TestEvidencePacketDataValidateRejectsBadAddresses(t *testing.T) {
	good := types.NewEvidencePacketData(sdk.ConsAddress([]byte("20-byte-cons-address")), 100, []byte{0x3F}, 8, 600, math.LegacyMustNewDecFromStr("0.5"))
	require.NoError(t, good.Validate())

	for _, size := range []int{0, 1, crypto.AddressSize - 1, crypto.AddressSize + 1, 32, 256} {
		bad := good
		bad.ValidatorAddr = make([]byte, size)
		require.ErrorIs(t, bad.Validate(), types.ErrInvalidPacketData, "a %d-byte address", size)
	}

	missing := good
	missing.ValidatorAddr = nil
	require.ErrorIs(t, missing.Validate(), types.ErrInvalidPacketData)
}
