package types_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

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

	// The SDK caches an address's bech32 string across prefix changes; the
	// test switches prefixes, so the cache is off for its duration.
	sdk.SetAddrCacheEnabled(false)
	t.Cleanup(func() { sdk.SetAddrCacheEnabled(true) })

	// Encode as the consumer, whose prefix is not the provider's.
	config := sdk.GetConfig()
	defaultAccPrefix, defaultValPrefix, defaultConsPrefix := config.GetBech32AccountAddrPrefix(), config.GetBech32ValidatorAddrPrefix(), config.GetBech32ConsensusAddrPrefix()
	config.SetBech32PrefixForConsensusNode("shardvalcons", "shardvalconspub")
	bz := packet.GetBytes()
	require.NotContains(t, string(bz), "shardvalcons",
		"the accused address must not cross the wire as a bech32 string")
	require.NotContains(t, string(bz), addr.String())

	// Decode as the provider.
	config.SetBech32PrefixForConsensusNode(defaultConsPrefix, defaultConsPrefix+"pub")
	t.Cleanup(func() {
		config.SetBech32PrefixForAccount(defaultAccPrefix, defaultAccPrefix+"pub")
		config.SetBech32PrefixForValidator(defaultValPrefix, defaultValPrefix+"pub")
		config.SetBech32PrefixForConsensusNode(defaultConsPrefix, defaultConsPrefix+"pub")
	})
	var decoded types.EvidencePacketData
	require.NoError(t, json.Unmarshal(bz, &decoded))
	require.NoError(t, decoded.Validate())
	require.Equal(t, addr, decoded.ConsAddress(), "the provider must resolve the same validator the consumer accused")
	require.True(t, strings.HasPrefix(decoded.ConsAddress().String(), defaultConsPrefix))
}

// TestEvidencePacketDataValidateRejectsBadAddresses covers the address check
// that replaced the empty-only one.
func TestEvidencePacketDataValidateRejectsBadAddresses(t *testing.T) {
	good := types.NewEvidencePacketData(sdk.ConsAddress([]byte("20-byte-cons-address")), 100, []byte{0x3F}, 8, 600, math.LegacyMustNewDecFromStr("0.5"))
	require.NoError(t, good.Validate())

	empty := good
	empty.ValidatorAddr = nil
	require.ErrorIs(t, empty.Validate(), types.ErrInvalidPacketData)

	tooLong := good
	tooLong.ValidatorAddr = make([]byte, 256)
	require.ErrorIs(t, tooLong.Validate(), types.ErrInvalidPacketData)
}
