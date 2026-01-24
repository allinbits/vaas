package types_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	sdk "github.com/cosmos/cosmos-sdk/types"

	cryptoutil "github.com/allinbits/vaas/testutil/crypto"
	providerkeeper "github.com/allinbits/vaas/x/vaas/provider/keeper"
	providertypes "github.com/allinbits/vaas/x/vaas/provider/types"
)

// Tests that all singular keys, or prefixes to fully resolves keys are non duplicate byte values.
func TestNoDuplicates(t *testing.T) {
	prefixes := providertypes.GetAllKeyPrefixes()
	seen := []byte{}

	for _, prefix := range prefixes {
		require.NotContains(t, seen, prefix, "Duplicate key prefix: %v", prefix)
		seen = append(seen, prefix)
	}
}

// Test that the value of all byte prefixes is preserved
func TestPreserveBytePrefix(t *testing.T) {
	i := 0
	require.Equal(t, byte(0xFF), providertypes.ParametersKey()[0])
	i++
	require.Equal(t, byte(0), providertypes.PortKey()[0])
	i++
	require.Equal(t, byte(1), providertypes.ValidatorSetUpdateIdKey()[0])
	i++
	require.Equal(t, byte(2), providertypes.ConsumerIdToChannelIdKey("13")[0])
	i++
	require.Equal(t, byte(3), providertypes.ChannelIdToConsumerIdKeyPrefix()[0])
	i++
	require.Equal(t, byte(4), providertypes.ConsumerIdToClientIdKeyPrefix()[0])
	i++
	require.Equal(t, byte(5), providertypes.ValsetUpdateBlockHeightKeyPrefix()[0])
	i++
	require.Equal(t, byte(6), providertypes.ConsumerGenesisKey("13")[0])
	i++
	require.Equal(t, byte(7), providertypes.InitChainHeightKey("13")[0])
	i++
	require.Equal(t, byte(8), providertypes.PendingVSCsKey("13")[0])
	i++
	require.Equal(t, byte(9), providertypes.ConsumerValidatorsKeyPrefix())
	i++
	require.Equal(t, byte(10), providertypes.ValidatorsByConsumerAddrKeyPrefix())
	i++
	require.Equal(t, byte(11), providertypes.EquivocationEvidenceMinHeightKey("13")[0])
	i++
	require.Equal(t, byte(12), providertypes.ConsumerValidatorKeyPrefix())
	i++
	require.Equal(t, byte(13), providertypes.ConsumerAddrsToPruneV2KeyPrefix())
	i++
	require.Equal(t, byte(14), providertypes.LastProviderConsensusValsPrefix()[0])
	i++
	require.Equal(t, byte(15), providertypes.ConsumerIdKey()[0])
	i++
	require.Equal(t, byte(16), providertypes.ConsumerIdToChainIdKey("13")[0])
	i++
	require.Equal(t, byte(17), providertypes.ConsumerIdToOwnerAddressKey("13")[0])
	i++
	require.Equal(t, byte(18), providertypes.ConsumerIdToMetadataKeyPrefix())
	i++
	require.Equal(t, byte(19), providertypes.ConsumerIdToInitializationParametersKeyPrefix())
	i++
	require.Equal(t, byte(20), providertypes.ConsumerIdToPowerShapingParametersKey("13")[0])
	i++
	require.Equal(t, byte(21), providertypes.ConsumerIdToPhaseKey("13")[0])
	i++
	require.Equal(t, byte(22), providertypes.ConsumerIdToRemovalTimeKeyPrefix())
	i++
	require.Equal(t, byte(23), providertypes.SpawnTimeToConsumerIdsKeyPrefix())
	i++
	require.Equal(t, byte(24), providertypes.RemovalTimeToConsumerIdsKeyPrefix())
	i++
	require.Equal(t, byte(25), providertypes.ClientIdToConsumerIdKey("clientId")[0])
	i++
	require.Equal(t, byte(26), providertypes.PrioritylistKeyPrefix())
	i++
	require.Equal(t, byte(27), providertypes.ConsumerIdToInfractionParametersKeyPrefix())
	i++
	require.Equal(t, byte(28), providertypes.ConsumerIdToQueuedInfractionParametersKeyPrefix())
	i++
	require.Equal(t, byte(29), providertypes.InfractionScheduledTimeToConsumerIdsKeyPrefix())
	i++

	prefixes := providertypes.GetAllKeyPrefixes()
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
		providertypes.ParametersKey(),
		providertypes.PortKey(),
		providertypes.ValidatorSetUpdateIdKey(),
		providertypes.ConsumerIdToChannelIdKey("13"),
		providertypes.ChannelToConsumerIdKey("channelID"),
		providertypes.ConsumerIdToClientIdKey("13"),
		providertypes.ValsetUpdateBlockHeightKey(7),
		providertypes.ConsumerGenesisKey("13"),
		providertypes.InitChainHeightKey("13"),
		providertypes.PendingVSCsKey("13"),
		providertypes.ConsumerValidatorsKey("13", providertypes.NewProviderConsAddress([]byte{0x05})),
		providertypes.ValidatorsByConsumerAddrKey("13", providertypes.NewConsumerConsAddress([]byte{0x05})),
		providertypes.EquivocationEvidenceMinHeightKey("13"),
		providertypes.ConsumerValidatorKey("13", providertypes.NewProviderConsAddress([]byte{0x05}).Address.Bytes()),
		providertypes.ConsumerAddrsToPruneV2Key("13", time.Time{}),
		providerkeeper.GetValidatorKey(providertypes.LastProviderConsensusValsPrefix(), providertypes.NewProviderConsAddress([]byte{0x05})),
		providertypes.ConsumerIdKey(),
		providertypes.ConsumerIdToChainIdKey("13"),
		providertypes.ConsumerIdToOwnerAddressKey("13"),
		providertypes.ConsumerIdToMetadataKey("13"),
		providertypes.ConsumerIdToInitializationParametersKey("13"),
		providertypes.ConsumerIdToPowerShapingParametersKey("13"),
		providertypes.ConsumerIdToPhaseKey("13"),
		providertypes.ConsumerIdToRemovalTimeKey("13"),
		providertypes.SpawnTimeToConsumerIdsKey(time.Time{}),
		providertypes.RemovalTimeToConsumerIdsKey(time.Time{}),
		providertypes.ClientIdToConsumerIdKey("clientId"),
		providertypes.PrioritylistKey("13", providertypes.NewProviderConsAddress([]byte{0x05})),
		providertypes.ConsumerIdToInfractionParametersKey("13"),
		providertypes.ConsumerIdToQueuedInfractionParametersKey("13"),
		providertypes.InfractionScheduledTimeToConsumerIdsKey(time.Time{}),
	}
}

// Tests the construction and parsing of StringIdAndTs keys
func TestStringIdAndTsKeyAndParse(t *testing.T) {
	tests := []struct {
		prefix     byte
		consumerID string
		timestamp  time.Time
	}{
		{prefix: 0x01, consumerID: "1", timestamp: time.Now()},
		{prefix: 0x02, consumerID: "111", timestamp: time.Date(
			2003, 11, 17, 20, 34, 58, 651387237, time.UTC)},
		{prefix: 0x03, consumerID: "2000", timestamp: time.Now().Add(5000 * time.Hour)},
	}

	for _, test := range tests {
		key := providertypes.StringIdAndTsKey(test.prefix, test.consumerID, test.timestamp)
		require.NotEmpty(t, key)
		// Expected bytes = prefix + consumerID length + consumerID + time bytes
		expectedLen := 1 + 8 + len(test.consumerID) + len(sdk.FormatTimeBytes(time.Time{}))
		require.Equal(t, expectedLen, len(key))
		parsedID, parsedTime, err := providertypes.ParseStringIdAndTsKey(test.prefix, key)
		require.Equal(t, test.consumerID, parsedID)
		require.Equal(t, test.timestamp.UTC(), parsedTime.UTC())
		require.NoError(t, err)
	}
}

// Tests the construction and parsing of StringIdAndUintId keys
func TestStringIdAndUintIdAndParse(t *testing.T) {
	tests := []struct {
		prefix     byte
		consumerID string
		uintID     uint64
	}{
		{prefix: 0x01, consumerID: "1", uintID: 1},
		{prefix: 0x02, consumerID: "13", uintID: 2},
		{prefix: 0x03, consumerID: "245", uintID: 3},
	}

	for _, test := range tests {
		key := providertypes.StringIdAndUintIdKey(test.prefix, test.consumerID, test.uintID)
		require.NotEmpty(t, key)
		// Expected bytes = prefix + consumerID length + consumerID + vscId bytes
		expectedLen := 1 + 8 + len(test.consumerID) + 8
		require.Equal(t, expectedLen, len(key))
		parsedChainID, parsedUintID, err := providertypes.ParseStringIdAndUintIdKey(test.prefix, key)
		require.Equal(t, test.consumerID, parsedChainID)
		require.Equal(t, test.uintID, parsedUintID)
		require.NoError(t, err)
	}
}

// Tests the construction and parsing of StringIdAndConsAddr keys
func TestStringIdAndConsAddrAndParse(t *testing.T) {
	cIds := []*cryptoutil.CryptoIdentity{
		cryptoutil.NewCryptoIdentityFromIntSeed(99998),
		cryptoutil.NewCryptoIdentityFromIntSeed(99999),
		cryptoutil.NewCryptoIdentityFromIntSeed(100000),
	}
	pubKey1 := cIds[0].TMCryptoPubKey()
	pubKey2 := cIds[1].TMCryptoPubKey()
	pubKey3 := cIds[2].TMCryptoPubKey()

	tests := []struct {
		prefix     byte
		consumerId string
		addr       sdk.ConsAddress
	}{
		{prefix: 0x01, consumerId: "1", addr: sdk.ConsAddress(pubKey1.Address())},
		{prefix: 0x02, consumerId: "23", addr: sdk.ConsAddress(pubKey2.Address())},
		{prefix: 0x03, consumerId: "456", addr: sdk.ConsAddress(pubKey3.Address())},
	}

	for _, test := range tests {
		key := providertypes.StringIdAndConsAddrKey(test.prefix, test.consumerId, test.addr)
		require.NotEmpty(t, key)
		// Expected bytes = prefix + consumerID length + consumerID + consAddr bytes
		expectedLen := 1 + 8 + len(test.consumerId) + len(test.addr)
		require.Equal(t, expectedLen, len(key))
		parsedID, parsedConsAddr, err := providertypes.ParseStringIdAndConsAddrKey(test.prefix, key)
		require.Equal(t, test.consumerId, parsedID)
		require.Equal(t, test.addr, parsedConsAddr)
		require.NoError(t, err)
	}
}

// Test key packing functions with the format <prefix><stringID>
func TestKeysWithPrefixAndId(t *testing.T) {
	funcs := []func(string) []byte{
		providertypes.ConsumerIdToChannelIdKey,
		providertypes.ChannelToConsumerIdKey,
		providertypes.ConsumerIdToClientIdKey,
		providertypes.ConsumerGenesisKey,
		providertypes.InitChainHeightKey,
		providertypes.PendingVSCsKey,
	}

	tests := []struct {
		stringID string
	}{
		{stringID: "test id 1"},
		{stringID: "2"},
		{stringID: "a longer id to test test test test test"},
	}

	for _, test := range tests {
		for _, function := range funcs {
			key := function(test.stringID)
			require.Equal(t, []byte(test.stringID), key[1:])
		}
	}
}

func TestKeysWithUint64Payload(t *testing.T) {
	funcs := []func(uint64) []byte{
		providertypes.ValsetUpdateBlockHeightKey,
	}

	tests := []struct {
		integer uint64
	}{
		{integer: 0},
		{integer: 25},
		{integer: 3472843},
		{integer: 8503458034859305834},
	}

	for _, test := range tests {
		for _, function := range funcs {
			key := function(test.integer)
			require.Equal(t, sdk.Uint64ToBigEndian(test.integer), key[1:])
		}
	}
}
