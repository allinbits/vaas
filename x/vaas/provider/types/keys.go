package types

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"sort"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"

	vaastypes "github.com/allinbits/vaas/x/vaas/types"
)

type Status int

const (
	// ModuleName defines the VAAS provider module name
	ModuleName = "provider"

	// StoreKey is the store key string for IBC transfer
	StoreKey = ModuleName

	// RouterKey is the message route for IBC transfer
	RouterKey = ModuleName

	// QuerierRoute is the querier route for IBC transfer
	QuerierRoute = ModuleName

	// Default validator set update ID
	DefaultValsetUpdateID = 1

	// Names for the store keys.
	// Used for storing the byte prefixes in the constant map.
	// See getKeyPrefixes().

	ParametersKeyName = "ParametersKey"

	PortKeyName = "PortKey"

	ValidatorSetUpdateIdKeyName = "ValidatorSetUpdateIdKey"

	ConsumerIdToChannelIdKeyName = "ConsumerIdToChannelIdKey"

	ChannelIdToConsumerIdKeyName = "ChannelToConsumerIdKey"

	ConsumerIdToClientIdKeyName = "ConsumerIdToClientIdKey"

	ValsetUpdateBlockHeightKeyName = "ValsetUpdateBlockHeightKey"

	ConsumerGenesisKeyName = "ConsumerGenesisKey"

	InitChainHeightKeyName = "InitChainHeightKey"

	PendingVSCsKeyName = "PendingVSCsKey"

	ConsumerValidatorsKeyName = "ConsumerValidatorsKey"

	ValidatorsByConsumerAddrKeyName = "ValidatorsByConsumerAddrKey"

	EquivocationEvidenceMinHeightKeyName = "EquivocationEvidenceMinHeightKey"

	ConsumerValidatorKeyName = "ConsumerValidatorKey"

	LastProviderConsensusValsKeyName = "LastProviderConsensusValsKey"

	ConsumerAddrsToPruneV2KeyName = "ConsumerAddrsToPruneV2Key"

	ConsumerIdKeyName = "ConsumerIdKey"

	ConsumerIdToChainIdKeyName = "ConsumerIdToChainIdKey"

	ConsumerIdToOwnerAddressKeyName = "ConsumerIdToOwnerAddress"

	ConsumerIdToConsumerMetadataKeyName = "ConsumerIdToMetadataKey"

	ConsumerIdToInitializationParametersKeyName = "ConsumerIdToInitializationParametersKey"

	ConsumerIdToPowerShapingParameters = "ConsumerIdToPowerShapingParametersKey"

	ConsumerIdToPhaseKeyName = "ConsumerIdToPhaseKey"

	ConsumerIdToRemovalTimeKeyName = "ConsumerIdToRemovalTimeKey"

	SpawnTimeToConsumerIdsKeyName = "SpawnTimeToConsumerIdsKeyName"

	RemovalTimeToConsumerIdsKeyName = "RemovalTimeToConsumerIdsKeyName"

	ClientIdToConsumerIdKeyName = "ClientIdToConsumerIdKey"

	PrioritylistKeyName = "PrioritylistKey"

	ConsumerIdToInfractionParametersKeyName = "ConsumerIdToInfractionParametersKey"

	ConsumerIdToQueuedInfractionParametersKeyName = "ConsumerIdToQueuedInfractionParametersKeyName"

	InfractionScheduledTimeToConsumerIdsKeyName = "InfractionScheduledTimeToConsumerIdsKeyName"
)

// getKeyPrefixes returns a constant map of all the byte prefixes for existing keys
func getKeyPrefixes() map[string]byte {
	return map[string]byte{
		// ParametersKey is the key for storing provider's parameters.
		// note that this was set to the max uint8 type value 0xFF in order to protect
		// from using the ICS v5.0.0 provider module by mistake
		ParametersKeyName: byte(0xFF),

		// PortKey defines the key to store the port ID in store
		PortKeyName: 0,

		// ValidatorSetUpdateIdKey is the key that stores the current validator set update id
		ValidatorSetUpdateIdKeyName: 1,

		// ConsumerIdToChannelIdKey is the key for storing mapping
		// from chainID to the channel ID that is used to send over validator set changes.
		ConsumerIdToChannelIdKeyName: 2,

		// ChannelToConsumerIdKey is the key for storing mapping
		// from the CCV channel ID to the consumer chain ID.
		ChannelIdToConsumerIdKeyName: 3,

		// ConsumerIdToClientIdKey is the key for storing the client ID for a given consumer chainID.
		ConsumerIdToClientIdKeyName: 4,

		// ValsetUpdateBlockHeightKey is the key for storing the mapping from vscIDs to block heights
		ValsetUpdateBlockHeightKeyName: 5,

		// ConsumerGenesisKey stores consumer genesis state material (consensus state and client state) indexed by consumer chain id
		ConsumerGenesisKeyName: 6,

		// InitChainHeightKey is the key for storing the mapping from a chain id to the corresponding block height on the provider
		// this consumer chain was initialized
		InitChainHeightKeyName: 7,

		// PendingVSCsKey is the key for storing pending ValidatorSetChangePacket data
		PendingVSCsKeyName: 8,

		// ConsumerValidatorsKey is the key for storing the validator assigned keys for every consumer chain
		ConsumerValidatorsKeyName: 9,

		// ValidatorsByConsumerAddrKey is the key for storing the mapping from validator addresses
		// on consumer chains to validator addresses on the provider chain
		ValidatorsByConsumerAddrKeyName: 10,

		// EquivocationEvidenceMinHeightKey is the key for storing the mapping from consumer chain IDs
		// to the minimum height of a valid consumer equivocation evidence
		EquivocationEvidenceMinHeightKeyName: 11,

		// ConsumerValidatorKey is the key for storing for each consumer chain all the consumer
		// validators in this epoch that are validating the consumer chain
		ConsumerValidatorKeyName: 12,

		// ConsumerAddrsToPruneV2Key is the key for storing
		// consumer validators addresses that need to be pruned.
		ConsumerAddrsToPruneV2KeyName: 13,

		// LastProviderConsensusValsKey is the key for storing the last validator set
		// sent to the consensus engine of the provider chain
		LastProviderConsensusValsKeyName: 14,

		// ConsumerIdKeyName is the key for storing the consumer id for the next registered consumer chain
		ConsumerIdKeyName: 15,

		// ConsumerIdToChainIdKeyName is the key for storing the chain id for the given consumer id
		ConsumerIdToChainIdKeyName: 16,

		// ConsumerIdToOwnerAddressKeyName is the key for storing the owner address for the given consumer id
		ConsumerIdToOwnerAddressKeyName: 17,

		// ConsumerIdToConsumerMetadataKeyName is the key for storing the metadata for the given consumer id
		ConsumerIdToConsumerMetadataKeyName: 18,

		// ConsumerIdToInitializationParametersKeyName is the key for storing the initialization parameters for the given consumer id
		ConsumerIdToInitializationParametersKeyName: 19,

		// ConsumerIdToPowerShapingParameters is the key for storing the power-shaping parameters for the given consumer id
		ConsumerIdToPowerShapingParameters: 20,

		// ConsumerIdToPhaseKeyName is the key for storing the phase of a consumer chain with the given consumer id
		ConsumerIdToPhaseKeyName: 21,

		// ConsumerIdToRemovalTimeKeyName is the key for storing the removal time of a consumer chain that is to be removed
		ConsumerIdToRemovalTimeKeyName: 22,

		// SpawnTimeToConsumerIdKeyName is the key for storing pending initialized consumers that are to be launched.
		// For a specific spawn time, it might store multiple consumer chain ids for chains that are to be launched.
		SpawnTimeToConsumerIdsKeyName: 23,

		// RemovalTimeToConsumerIdsKeyName is the key for storing pending launched consumers that are to be removed.
		// For a specific removal time, it might store multiple consumer chain ids for chains that are to be removed.
		RemovalTimeToConsumerIdsKeyName: 24,

		// ClientIdToConsumerIdKeyName is the key for storing the consumer id for the given client id
		ClientIdToConsumerIdKeyName: 25,

		// PrioritylistKey is the key for storing the mapping from a consumer chain to the set of validators that are
		// prioritylisted.
		PrioritylistKeyName: 26,

		// ConsumerIdToInfractionParametersKeyName is the key for storing slashing and jailing infraction parameters for a specific consumer chain
		ConsumerIdToInfractionParametersKeyName: 27,

		// ConsumerIdToQueuedInfractionParametersKeyName is the key for storing queued infraction parameters that will be used to update consumer infraction parameters
		ConsumerIdToQueuedInfractionParametersKeyName: 28,

		// InfractionScheduledTimeToConsumerIdsKeyName is the key for storing time when the infraction parameters will be updated for the specific consumer
		InfractionScheduledTimeToConsumerIdsKeyName: 29,

		// NOTE: DO NOT ADD NEW BYTE PREFIXES HERE WITHOUT ADDING THEM TO TestPreserveBytePrefix() IN keys_test.go
	}
}

// mustGetKeyPrefix returns the key prefix for a given key.
// It panics if there is not byte prefix for the index.
func mustGetKeyPrefix(key string) byte {
	keyPrefixes := getKeyPrefixes()
	if prefix, found := keyPrefixes[key]; !found {
		panic(fmt.Sprintf("could not find key prefix for index %s", key))
	} else {
		return prefix
	}
}

// GetAllKeyPrefixes returns all the key prefixes.
// Only used for testing
func GetAllKeyPrefixes() []byte {
	prefixMap := getKeyPrefixes()
	keys := make([]string, 0, len(prefixMap))
	for k := range prefixMap {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	prefixList := make([]byte, 0, len(prefixMap))
	for _, k := range keys {
		prefixList = append(prefixList, prefixMap[k])
	}
	return prefixList
}

// GetAllKeyNames returns the names of all the keys.
// Only used for testing
func GetAllKeyNames() []string {
	prefixMap := getKeyPrefixes()
	keys := make([]string, 0, len(prefixMap))
	for k := range prefixMap {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

//
// Fully defined key func section
//

// ParametersKey returns the key for the parameters of the provider module in the store
func ParametersKey() []byte {
	return []byte{mustGetKeyPrefix(ParametersKeyName)}
}

// PortKey returns the key to the port ID in the store
func PortKey() []byte {
	return []byte{mustGetKeyPrefix(PortKeyName)}
}

// ValidatorSetUpdateIdKey is the key that stores the current validator set update id
func ValidatorSetUpdateIdKey() []byte {
	return []byte{mustGetKeyPrefix(ValidatorSetUpdateIdKeyName)}
}

// ConsumerIdToChannelIdKey returns the key under which the CCV channel ID will be stored for the given consumer chain.
func ConsumerIdToChannelIdKey(consumerId string) []byte {
	return append([]byte{mustGetKeyPrefix(ConsumerIdToChannelIdKeyName)}, []byte(consumerId)...)
}

// ChannelIdToConsumerIdKeyPrefix returns the key prefix for storing the consumer chain ids.
func ChannelIdToConsumerIdKeyPrefix() []byte {
	return []byte{mustGetKeyPrefix(ChannelIdToConsumerIdKeyName)}
}

// ChannelToConsumerIdKey returns the key under which the consumer chain id will be stored for the given channelId.
func ChannelToConsumerIdKey(channelId string) []byte {
	return append(ChannelIdToConsumerIdKeyPrefix(), []byte(channelId)...)
}

// ConsumerIdToClientIdKeyPrefix returns the key prefix for storing the clientId for the given consumerId.
func ConsumerIdToClientIdKeyPrefix() []byte {
	return []byte{mustGetKeyPrefix(ConsumerIdToClientIdKeyName)}
}

// ConsumerIdToClientIdKey returns the key under which the clientId for the given consumerId is stored.
func ConsumerIdToClientIdKey(consumerId string) []byte {
	return append(ConsumerIdToClientIdKeyPrefix(), []byte(consumerId)...)
}

// ValsetUpdateBlockHeightKeyPrefix returns the key prefix that storing the mapping from valset update ID to block height
func ValsetUpdateBlockHeightKeyPrefix() []byte {
	return []byte{mustGetKeyPrefix(ValsetUpdateBlockHeightKeyName)}
}

// ValsetUpdateBlockHeightKey returns the key that storing the mapping from valset update ID to block height
func ValsetUpdateBlockHeightKey(valsetUpdateId uint64) []byte {
	vuidBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(vuidBytes, valsetUpdateId)
	return append(ValsetUpdateBlockHeightKeyPrefix(), vuidBytes...)
}

// ConsumerGenesisKey returns the key corresponding to consumer genesis state material
// (consensus state and client state) indexed by consumer id
func ConsumerGenesisKey(consumerId string) []byte {
	return append([]byte{mustGetKeyPrefix(ConsumerGenesisKeyName)}, []byte(consumerId)...)
}

// InitChainHeightKey returns the key under which the block height for a given consumer id is stored
func InitChainHeightKey(consumerId string) []byte {
	return append([]byte{mustGetKeyPrefix(InitChainHeightKeyName)}, []byte(consumerId)...)
}

// PendingVSCsKey returns the key under which
// pending ValidatorSetChangePacket data is stored for a given consumer id
func PendingVSCsKey(consumerId string) []byte {
	return append([]byte{mustGetKeyPrefix(PendingVSCsKeyName)}, []byte(consumerId)...)
}

// ConsumerValidatorsKey returns the key for storing the validator assigned keys for every consumer chain
func ConsumerValidatorsKeyPrefix() byte {
	return mustGetKeyPrefix(ConsumerValidatorsKeyName)
}

// ConsumerValidatorsKey returns the key under which the
// validator assigned keys for every consumer chain are stored
func ConsumerValidatorsKey(consumerId string, addr ProviderConsAddress) []byte {
	return StringIdAndConsAddrKey(ConsumerValidatorsKeyPrefix(), consumerId, addr.ToSdkConsAddr())
}

// ValidatorsByConsumerAddrKeyPrefix returns the key prefix for storing the mapping from validator addresses
// on consumer chains to validator addresses on the provider chain
func ValidatorsByConsumerAddrKeyPrefix() byte {
	return mustGetKeyPrefix(ValidatorsByConsumerAddrKeyName)
}

// ValidatorsByConsumerAddrKey returns the key for storing the mapping from validator addresses
// on consumer chains to validator addresses on the provider chain
func ValidatorsByConsumerAddrKey(consumerId string, addr ConsumerConsAddress) []byte {
	return StringIdAndConsAddrKey(ValidatorsByConsumerAddrKeyPrefix(), consumerId, addr.ToSdkConsAddr())
}

// EquivocationEvidenceMinHeightKey returns the key storing the minimum height
// of a valid consumer equivocation evidence for a given consumer id
func EquivocationEvidenceMinHeightKey(consumerId string) []byte {
	return append([]byte{mustGetKeyPrefix(EquivocationEvidenceMinHeightKeyName)}, []byte(consumerId)...)
}

// ConsumerValidatorKeyPrefix returns the key prefix for storing consumer validators
func ConsumerValidatorKeyPrefix() byte {
	return mustGetKeyPrefix(ConsumerValidatorKeyName)
}

// ConsumerValidatorKey returns the key for storing consumer validators
// for the given consumer chain `consumerId` and validator with `providerAddr`
func ConsumerValidatorKey(consumerId string, providerAddr []byte) []byte {
	return StringIdAndConsAddrKey(ConsumerValidatorKeyPrefix(), consumerId, sdk.ConsAddress(providerAddr))
}

// PrioritylistKeyPrefix returns the key prefix for storing consumer chains prioritylists
func PrioritylistKeyPrefix() byte {
	return mustGetKeyPrefix(PrioritylistKeyName)
}

// PrioritylistKey returns the key for storing consumer chains prioritylists
func PrioritylistKey(consumerId string, providerAddr ProviderConsAddress) []byte {
	return StringIdAndConsAddrKey(PrioritylistKeyPrefix(), consumerId, providerAddr.ToSdkConsAddr())
}

// ConsumerAddrsToPruneV2KeyPrefix returns the key prefix for storing the consumer validators
// addresses that need to be pruned. These are stored as a
// (chainID, ts) -> (consumer_address1, consumer_address2, ...) mapping, where ts is the
// timestamp at which the consumer validators addresses can be pruned.
func ConsumerAddrsToPruneV2KeyPrefix() byte {
	return mustGetKeyPrefix(ConsumerAddrsToPruneV2KeyName)
}

// ConsumerAddrsToPruneV2Key returns the key for storing the consumer validators
// addresses that need to be pruned.
func ConsumerAddrsToPruneV2Key(consumerId string, pruneTs time.Time) []byte {
	return StringIdAndTsKey(ConsumerAddrsToPruneV2KeyPrefix(), consumerId, pruneTs)
}

// LastProviderConsensusValsPrefix returns the key prefix for storing the last validator set sent to the consensus engine of the provider chain
func LastProviderConsensusValsPrefix() []byte {
	return []byte{mustGetKeyPrefix(LastProviderConsensusValsKeyName)}
}

// ConsumerIdKey returns the key used to store the consumerId of the next registered chain
func ConsumerIdKey() []byte {
	return []byte{mustGetKeyPrefix(ConsumerIdKeyName)}
}

// ConsumerIdToChainIdKey returns the key used to store the chain id of this consumer id
func ConsumerIdToChainIdKey(consumerId string) []byte {
	return StringIdWithLenKey(mustGetKeyPrefix(ConsumerIdToChainIdKeyName), consumerId)
}

// ConsumerIdToOwnerAddressKey returns the owner address of this consumer id
func ConsumerIdToOwnerAddressKey(consumerId string) []byte {
	return StringIdWithLenKey(mustGetKeyPrefix(ConsumerIdToOwnerAddressKeyName), consumerId)
}

// ConsumerIdToMetadataKeyPrefix returns the key prefix for storing consumer metadata
func ConsumerIdToMetadataKeyPrefix() byte {
	return mustGetKeyPrefix(ConsumerIdToConsumerMetadataKeyName)
}

// ConsumerIdToMetadataKey returns the key used to store the metadata that corresponds to this consumer id
func ConsumerIdToMetadataKey(consumerId string) []byte {
	return StringIdWithLenKey(ConsumerIdToMetadataKeyPrefix(), consumerId)
}

// ConsumerIdToInitializationParametersKeyPrefix returns the key prefix for storing consumer initialization parameters
func ConsumerIdToInitializationParametersKeyPrefix() byte {
	return mustGetKeyPrefix(ConsumerIdToInitializationParametersKeyName)
}

// ConsumerIdToInitializationParametersKey returns the key used to store the initialization parameters that corresponds to this consumer id
func ConsumerIdToInitializationParametersKey(consumerId string) []byte {
	return StringIdWithLenKey(ConsumerIdToInitializationParametersKeyPrefix(), consumerId)
}

// ConsumerIdToPowerShapingParametersKey returns the key used to store the power-shaping parameters that corresponds to this consumer id
func ConsumerIdToPowerShapingParametersKey(consumerId string) []byte {
	return StringIdWithLenKey(mustGetKeyPrefix(ConsumerIdToPowerShapingParameters), consumerId)
}

// ConsumerIdToPhaseKeyPrefix returns the key prefix used to iterate over all the consumer ids and their phases.
func ConsumerIdToPhaseKeyPrefix() byte {
	return mustGetKeyPrefix(ConsumerIdToPhaseKeyName)
}

// ConsumerIdToPhaseKey returns the key used to store the phase that corresponds to this consumer id
func ConsumerIdToPhaseKey(consumerId string) []byte {
	return StringIdWithLenKey(ConsumerIdToPhaseKeyPrefix(), consumerId)
}

// ConsumerIdToRemovalTimeKeyPrefix returns the key prefix for storing the removal times of consumer chains
// that are about to be removed
func ConsumerIdToRemovalTimeKeyPrefix() byte {
	return mustGetKeyPrefix(ConsumerIdToRemovalTimeKeyName)
}

// ConsumerIdToRemovalTimeKey returns the key used to store the removal time that corresponds to a to-be-removed chain with consumer id
func ConsumerIdToRemovalTimeKey(consumerId string) []byte {
	return StringIdWithLenKey(ConsumerIdToRemovalTimeKeyPrefix(), consumerId)
}

// SpawnTimeToConsumerIdsKeyPrefix returns the key prefix for storing pending chains that are to be launched
func SpawnTimeToConsumerIdsKeyPrefix() byte {
	return mustGetKeyPrefix(SpawnTimeToConsumerIdsKeyName)
}

// SpawnTimeToConsumerIdsKey returns the key prefix for storing the spawn times of consumer chains
// that are about to be launched
func SpawnTimeToConsumerIdsKey(spawnTime time.Time) []byte {
	return vaastypes.AppendMany(
		// append the prefix
		[]byte{SpawnTimeToConsumerIdsKeyPrefix()},
		// append the time
		sdk.FormatTimeBytes(spawnTime),
	)
}

// RemovalTimeToConsumerIdsKeyPrefix returns the key prefix for storing pending chains that are to be removed
func RemovalTimeToConsumerIdsKeyPrefix() byte {
	return mustGetKeyPrefix(RemovalTimeToConsumerIdsKeyName)
}

// RemovalTimeToConsumerIdsKey returns the key prefix for storing the removal times of consumer chains
// that are about to be removed
func RemovalTimeToConsumerIdsKey(removalTime time.Time) []byte {
	return vaastypes.AppendMany(
		// append the prefix
		[]byte{RemovalTimeToConsumerIdsKeyPrefix()},
		// append the time
		sdk.FormatTimeBytes(removalTime),
	)
}

// ParseTime returns the marshalled time
func ParseTime(prefix byte, bz []byte) (time.Time, error) {
	expectedPrefix := []byte{prefix}
	prefixL := len(expectedPrefix)
	if prefix := bz[:prefixL]; !bytes.Equal(prefix, expectedPrefix) {
		return time.Time{}, fmt.Errorf("invalid prefix; expected: %X, got: %X", expectedPrefix, prefix)
	}
	timestamp, err := sdk.ParseTimeBytes(bz[prefixL:])
	if err != nil {
		return time.Time{}, err
	}
	return timestamp, nil
}

// ClientIdToConsumerIdKey returns the consumer id that corresponds to this client id
func ClientIdToConsumerIdKey(clientId string) []byte {
	clientIdLength := len(clientId)
	return vaastypes.AppendMany(
		// Append the prefix
		[]byte{mustGetKeyPrefix(ClientIdToConsumerIdKeyName)},
		// Append the client id length
		sdk.Uint64ToBigEndian(uint64(clientIdLength)),
		// Append the client id
		[]byte(clientId),
	)
}

// ConsumerIdToInfractionParametersKeyPrefix returns the key prefix for storing consumer infraction parameters
func ConsumerIdToInfractionParametersKeyPrefix() byte {
	return mustGetKeyPrefix(ConsumerIdToInfractionParametersKeyName)
}

// ConsumerIdToInfractionParametersKey returns the key used to store the infraction parameters that corresponds to this consumer id
func ConsumerIdToInfractionParametersKey(consumerId string) []byte {
	return StringIdWithLenKey(ConsumerIdToInfractionParametersKeyPrefix(), consumerId)
}

// ConsumerIdToQueuedInfractionParametersKeyPrefix returns the key prefix for storing queued consumer infraction parameters that will be applied after due time
func ConsumerIdToQueuedInfractionParametersKeyPrefix() byte {
	return mustGetKeyPrefix(ConsumerIdToQueuedInfractionParametersKeyName)
}

// ConsumerIdToQueuedInfractionParametersKey returns the key used to store the queued consumer infraction parameters that will be applied after due time
func ConsumerIdToQueuedInfractionParametersKey(consumerId string) []byte {
	return StringIdWithLenKey(ConsumerIdToQueuedInfractionParametersKeyPrefix(), consumerId)
}

// InfractionScheduledTimeToConsumerIdsKeyPrefix returns the key prefix for storing pending consumers ids that needs to update their infraction parameters at the specific time
func InfractionScheduledTimeToConsumerIdsKeyPrefix() byte {
	return mustGetKeyPrefix(InfractionScheduledTimeToConsumerIdsKeyName)
}

// InfractionScheduledTimeToConsumerIdsKey returns the key prefix for storing pending consumers ids that needs to update their infraction parameters at the specific time
func InfractionScheduledTimeToConsumerIdsKey(updateTime time.Time) []byte {
	return vaastypes.AppendMany(
		// append the prefix
		[]byte{InfractionScheduledTimeToConsumerIdsKeyPrefix()},
		// append the time
		sdk.FormatTimeBytes(updateTime),
	)
}

// NOTE: DO	NOT ADD FULLY DEFINED KEY FUNCTIONS WITHOUT ADDING THEM TO getAllFullyDefinedKeys() IN keys_test.go

//
// End of fully defined key func section
//

//
// Generic helpers section
//

// StringIdAndTsKey returns the key with the following format:
// bytePrefix | len(stringId) | stringId | timestamp
func StringIdAndTsKey(prefix byte, stringId string, timestamp time.Time) []byte {
	return vaastypes.AppendMany(
		StringIdWithLenKey(prefix, stringId),
		sdk.FormatTimeBytes(timestamp),
	)
}

// ParseStringIdAndTsKey returns the string id and time for a StringIdAndTs key
func ParseStringIdAndTsKey(prefix byte, bz []byte) (string, time.Time, error) {
	expectedPrefix := []byte{prefix}
	prefixL := len(expectedPrefix)
	if prefix := bz[:prefixL]; !bytes.Equal(prefix, expectedPrefix) {
		return "", time.Time{}, fmt.Errorf("invalid prefix; expected: %X, got: %X", expectedPrefix, prefix)
	}
	stringIdL := sdk.BigEndianToUint64(bz[prefixL : prefixL+8])
	stringId := string(bz[prefixL+8 : prefixL+8+int(stringIdL)])
	timestamp, err := sdk.ParseTimeBytes(bz[prefixL+8+int(stringIdL):])
	if err != nil {
		return "", time.Time{}, err
	}
	return stringId, timestamp, nil
}

// StringIdWithLenKey returns the key with the following format:
// bytePrefix | len(stringId) | stringId
func StringIdWithLenKey(prefix byte, stringId string) []byte {
	return vaastypes.AppendMany(
		// Append the prefix
		[]byte{prefix},
		// Append the string id length
		sdk.Uint64ToBigEndian(uint64(len(stringId))),
		// Append the string id
		[]byte(stringId),
	)
}

// ParseStringIdWithLenKey returns the stringId of a StringIdWithLen key
func ParseStringIdWithLenKey(prefix byte, bz []byte) (string, error) {
	expectedPrefix := []byte{prefix}
	prefixL := len(expectedPrefix)
	if prefixBz := bz[:prefixL]; !bytes.Equal(prefixBz, expectedPrefix) {
		return "", fmt.Errorf("invalid prefix; expected: %X, got: %X, input %X -- len %d", expectedPrefix, prefixBz, prefix, prefixL)
	}
	stringIdL := sdk.BigEndianToUint64(bz[prefixL : prefixL+8])
	stringId := string(bz[prefixL+8 : prefixL+8+int(stringIdL)])
	return stringId, nil
}

// StringIdAndUintIdKey returns the key with the following format:
// bytePrefix | len(stringId) | stringId | uint64(ID)
func StringIdAndUintIdKey(prefix byte, stringId string, uintId uint64) []byte {
	return vaastypes.AppendMany(
		StringIdWithLenKey(prefix, stringId),
		sdk.Uint64ToBigEndian(uintId),
	)
}

// ParseStringIdAndUintIdKey returns the string ID and uint ID for a StringIdAndUintId key
func ParseStringIdAndUintIdKey(prefix byte, bz []byte) (string, uint64, error) {
	expectedPrefix := []byte{prefix}
	prefixL := len(expectedPrefix)
	if prefix := bz[:prefixL]; !bytes.Equal(prefix, expectedPrefix) {
		return "", 0, fmt.Errorf("invalid prefix; expected: %X, got: %X", expectedPrefix, prefix)
	}
	stringIdL := sdk.BigEndianToUint64(bz[prefixL : prefixL+8])
	stringId := string(bz[prefixL+8 : prefixL+8+int(stringIdL)])
	uintID := sdk.BigEndianToUint64(bz[prefixL+8+int(stringIdL):])
	return stringId, uintID, nil
}

// StringIdAndConsAddrKey returns the key with the following format:
// bytePrefix | len(stringId) | stringId | ConsAddress
func StringIdAndConsAddrKey(prefix byte, stringId string, addr sdk.ConsAddress) []byte {
	return vaastypes.AppendMany(
		StringIdWithLenKey(prefix, stringId),
		addr,
	)
}

// ParseStringIdAndConsAddrKey returns the string ID and ConsAddress for a StringIdAndConsAddr key
func ParseStringIdAndConsAddrKey(prefix byte, bz []byte) (string, sdk.ConsAddress, error) {
	expectedPrefix := []byte{prefix}
	prefixL := len(expectedPrefix)
	if prefix := bz[:prefixL]; !bytes.Equal(prefix, expectedPrefix) {
		return "", nil, fmt.Errorf("invalid prefix; expected: %X, got: %X", expectedPrefix, prefix)
	}
	stringIdL := sdk.BigEndianToUint64(bz[prefixL : prefixL+8])
	stringId := string(bz[prefixL+8 : prefixL+8+int(stringIdL)])
	addr := bz[prefixL+8+int(stringIdL):]
	return stringId, addr, nil
}

//
// End of generic helpers section
//
