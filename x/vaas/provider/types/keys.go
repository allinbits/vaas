package types

import (
	"cosmossdk.io/collections"
)

type Status int

const (
	// ModuleName defines the VAAS provider module name
	ModuleName = "provider"

	// StoreKey is the store key string for IBC provider
	StoreKey = ModuleName

	// RouterKey is the message route for IBC transfer
	RouterKey = ModuleName

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

	ConsumerAddrsToPruneKeyName = "ConsumerAddrsToPruneKey"

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
)

// Collection key prefixes for use with cosmossdk.io/collections
var (
	PortPrefix                             = collections.NewPrefix(0)
	ValidatorSetUpdateIdPrefix             = collections.NewPrefix(1)
	ConsumerIdToChannelIdPrefix            = collections.NewPrefix(2)
	ChannelIdToConsumerIdPrefix            = collections.NewPrefix(3)
	ConsumerIdToClientIdPrefix             = collections.NewPrefix(4)
	ValsetUpdateBlockHeightPrefix          = collections.NewPrefix(5)
	ConsumerGenesisPrefix                  = collections.NewPrefix(6)
	InitChainHeightPrefix                  = collections.NewPrefix(7)
	PendingVSCsPrefix                      = collections.NewPrefix(8)
	ConsumerValidatorsPrefix               = collections.NewPrefix(9)
	ValidatorsByConsumerAddrPrefix         = collections.NewPrefix(10)
	EquivocationEvidenceMinHeightPrefix    = collections.NewPrefix(11)
	ConsumerValidatorPrefix                = collections.NewPrefix(12)
	ConsumerAddrsToPrunePrefix             = collections.NewPrefix(13)
	LastProviderConsensusVals              = collections.NewPrefix(14)
	ConsumerIdPrefix                       = collections.NewPrefix(15)
	ConsumerIdToChainIdPrefix              = collections.NewPrefix(16)
	ConsumerIdToOwnerAddressPrefix         = collections.NewPrefix(17)
	ConsumerIdToMetadataPrefix             = collections.NewPrefix(18)
	ConsumerIdToInitializationParamsPrefix = collections.NewPrefix(19)
	ConsumerIdToPowerShapingParamsPrefix   = collections.NewPrefix(20)
	ConsumerIdToPhasePrefix                = collections.NewPrefix(21)
	ConsumerIdToRemovalTimePrefix          = collections.NewPrefix(22)
	SpawnTimeToConsumerIdsPrefix           = collections.NewPrefix(23)
	RemovalTimeToConsumerIdsPrefix         = collections.NewPrefix(24)
	ClientIdToConsumerIdPrefix             = collections.NewPrefix(25)
	PriorityListPrefix                     = collections.NewPrefix(26)
	ParametersPrefix                       = collections.NewPrefix(0xFF)
)
