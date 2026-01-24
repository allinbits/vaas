package types

import (
	"encoding/binary"
	fmt "fmt"
	"sort"
)

const (
	// ModuleName defines the CCV consumer module name
	ModuleName = "ccvconsumer"

	// StoreKey is the store key string for IBC consumer
	StoreKey = ModuleName

	// RouterKey is the message route for IBC consumer
	RouterKey = ModuleName

	// QuerierRoute is the querier route for IBC consumer
	QuerierRoute = ModuleName

	// Names for the store keys.
	// Used for storing the byte prefixes in the constant map.
	// See getKeyPrefixes().

	PortKeyName = "PortKey"

	UnbondingTimeKeyName = "UnbondingTimeKey"

	ProviderClientIDKeyName = "ProviderClientIDKey"

	ProviderChannelIDKeyName = "ProviderChannelIDKey"

	PendingChangesKeyName = "PendingChangesKey"

	PreVAASKeyName = "PreVAASKey"

	InitialValSetKeyName = "InitialValSetKey"

	HistoricalInfoKeyName = "HistoricalInfoKey"

	HeightValsetUpdateIDKeyName = "HeightValsetUpdateIDKey"

	CrossChainValidatorKeyName = "CrossChainValidatorKey"

	InitGenesisHeightKeyName = "InitGenesisHeightKey"

	PrevStandaloneChainKeyName = "PrevStandaloneChainKey"

	ParametersKeyName = "ParametersKey"
)

// getKeyPrefixes returns a constant map of all the byte prefixes for existing keys
func getKeyPrefixes() map[string]byte {
	return map[string]byte{
		// PortKey is the key for storing the port ID
		PortKeyName: 0,

		// UnbondingTimeKey is the key for storing the unbonding period
		UnbondingTimeKeyName: 2,

		// ProviderClientIDKey is the key for storing the clientID of the provider client
		ProviderClientIDKeyName: 3,

		// ProviderChannelIDKey is the key for storing the channelID of the CCV channel
		ProviderChannelIDKeyName: 4,

		// PendingChangesKey is the key for storing any pending validator set changes
		// received over CCV channel but not yet flushed over ABCI
		PendingChangesKeyName: 5,

		// PreVAASKey is the key for storing the preVAAS flag, which is set to true
		// during the process of a standalone to consumer changeover.
		PreVAASKeyName: 7,

		// InitialValSetKey is the key for storing the initial validator set for a consumer
		InitialValSetKeyName: 8,

		// HistoricalInfoKey is the key for storing the historical info for a given height
		HistoricalInfoKeyName: 11,

		// HeightValsetUpdateIDKey is the key for storing the mapping from block height to valset update ID
		HeightValsetUpdateIDKeyName: 13,

		// CrossChainValidatorKey is the key for storing cross-chain validators by consensus address
		CrossChainValidatorKeyName: 16,

		// InitGenesisHeightKey is the key for storing the init genesis height
		InitGenesisHeightKeyName: 17,

		// PrevStandaloneChainKey is the key for storing the flag marking whether this chain was previously standalone
		PrevStandaloneChainKeyName: 19,

		// ParametersKey is the key for storing the consumer's parameters.
		ParametersKeyName: 22,

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

// PortKey returns the key for storing the port ID
func PortKey() []byte {
	return []byte{mustGetKeyPrefix(PortKeyName)}
}

// UnbondingTimeKey returns the key for storing the unbonding period
// TODO remove as it's never used
func UnbondingTimeKey() []byte {
	return []byte{mustGetKeyPrefix(UnbondingTimeKeyName)}
}

// ProviderClientIDKey returns the key for storing clientID of the provider
func ProviderClientIDKey() []byte {
	return []byte{mustGetKeyPrefix(ProviderClientIDKeyName)}
}

// ProviderChannelIDKey returns the key for storing channelID of the provider chain
func ProviderChannelIDKey() []byte {
	return []byte{mustGetKeyPrefix(ProviderChannelIDKeyName)}
}

// PendingChangesKey returns the key for storing pending validator set changes
func PendingChangesKey() []byte {
	return []byte{mustGetKeyPrefix(PendingChangesKeyName)}
}

// PreVAASKey returns the key for storing the preVAAS flag, which is set to true
// during the process of a standalone to consumer changeover.
func PreVAASKey() []byte {
	return []byte{mustGetKeyPrefix(PreVAASKeyName)}
}

// InitialValSetKey returns the key for storing the initial validator set for a consumer
func InitialValSetKey() []byte {
	return []byte{mustGetKeyPrefix(InitialValSetKeyName)}
}

// HistoricalInfoKeyPrefix the key prefix for storing the historical info for a given height
func HistoricalInfoKeyPrefix() []byte {
	return []byte{mustGetKeyPrefix(HistoricalInfoKeyName)}
}

// HistoricalInfoKey returns the key for storing the historical info for a given height
func HistoricalInfoKey(height int64) []byte {
	hBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(hBytes, uint64(height))
	return append(HistoricalInfoKeyPrefix(), hBytes...)
}

// HeightValsetUpdateIDKeyPrefix returns the key for storing a valset update ID for a given block height
func HeightValsetUpdateIDKeyPrefix() []byte {
	return []byte{mustGetKeyPrefix(HeightValsetUpdateIDKeyName)}
}

// HeightValsetUpdateIDKey returns the key for storing a valset update ID for a given block height
func HeightValsetUpdateIDKey(height uint64) []byte {
	hBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(hBytes, height)
	return append(HeightValsetUpdateIDKeyPrefix(), hBytes...)
}

// CrossChainValidatorKeyPrefix returns the key prefix for storing a cross chain validator by consensus address
func CrossChainValidatorKeyPrefix() []byte {
	return []byte{mustGetKeyPrefix(CrossChainValidatorKeyName)}
}

// CrossChainValidatorKey returns the key for storing a cross chain validator by consensus address
func CrossChainValidatorKey(addr []byte) []byte {
	return append(CrossChainValidatorKeyPrefix(), addr...)
}

// InitGenesisHeightKey returns the key for storing the init genesis height
func InitGenesisHeightKey() []byte {
	return []byte{mustGetKeyPrefix(InitGenesisHeightKeyName)}
}

// PrevStandaloneChainKey returns the key for storing the flag marking whether this chain was previously standalone
func PrevStandaloneChainKey() []byte {
	return []byte{mustGetKeyPrefix(PrevStandaloneChainKeyName)}
}

// ParametersKey returns the key for storing the consumer parameters
func ParametersKey() []byte {
	return []byte{mustGetKeyPrefix(ParametersKeyName)}
}

// NOTE: DO	NOT ADD FULLY DEFINED KEY FUNCTIONS WITHOUT ADDING THEM TO getAllFullyDefinedKeys() IN keys_test.go

//
// End of fully defined key func section
//
