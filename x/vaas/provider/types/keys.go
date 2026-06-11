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

	ValidatorSetUpdateIdKeyName = "ValidatorSetUpdateIdKey"

	// ConsumerIdToClientIdKeyName stores the mapping from consumer ID to client ID.
	// This is the primary lookup mechanism for IBC v2 client-based communication.
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

	ConsumerIdToPhaseKeyName = "ConsumerIdToPhaseKey"

	ConsumerIdToRemovalTimeKeyName = "ConsumerIdToRemovalTimeKey"

	SpawnTimeToConsumerIdsKeyName = "SpawnTimeToConsumerIdsKeyName"

	RemovalTimeToConsumerIdsKeyName = "RemovalTimeToConsumerIdsKeyName"

	// ClientIdToConsumerIdKeyName stores the reverse mapping from client ID to consumer ID.
	// This is the reverse lookup mechanism for IBC v2 client-based communication.
	ClientIdToConsumerIdKeyName = "ClientIdToConsumerIdKey"

	ConsumerIdToDebtKeyName = "ConsumerIdToDebtKeyName"

	ConsumerIdToFeesPerBlockOverrideKeyName = "ConsumerIdToFeesPerBlockOverrideKey"

	ConsumerFeePoolSharesKeyName      = "ConsumerFeePoolSharesKey"
	ConsumerFeePoolTotalSharesKeyName = "ConsumerFeePoolTotalSharesKey"
	FeePoolAddressToConsumerIdKeyName = "FeePoolAddressToConsumerIdKey"

	EpochDowntimeKeyName = "EpochDowntimeKey"
)

// Collection key prefixes for use with cosmossdk.io/collections
var (
	ValidatorSetUpdateIdPrefix             = collections.NewPrefix(0)
	ConsumerIdToClientIdPrefix             = collections.NewPrefix(1)
	ValsetUpdateBlockHeightPrefix          = collections.NewPrefix(2)
	ConsumerGenesisPrefix                  = collections.NewPrefix(3)
	InitChainHeightPrefix                  = collections.NewPrefix(4)
	PendingVSCsPrefix                      = collections.NewPrefix(5)
	ConsumerValidatorsPrefix               = collections.NewPrefix(6)
	ValidatorsByConsumerAddrPrefix         = collections.NewPrefix(7)
	EquivocationEvidenceMinHeightPrefix    = collections.NewPrefix(8)
	ConsumerValidatorPrefix                = collections.NewPrefix(9)
	ConsumerAddrsToPrunePrefix             = collections.NewPrefix(10)
	LastProviderConsensusVals              = collections.NewPrefix(11)
	ConsumerIdPrefix                       = collections.NewPrefix(12)
	ConsumerIdToChainIdPrefix              = collections.NewPrefix(13)
	ConsumerIdToOwnerAddressPrefix         = collections.NewPrefix(14)
	ConsumerIdToMetadataPrefix             = collections.NewPrefix(15)
	ConsumerIdToInitializationParamsPrefix = collections.NewPrefix(16)
	ConsumerIdToPhasePrefix                = collections.NewPrefix(17)
	ConsumerIdToRemovalTimePrefix          = collections.NewPrefix(18)
	SpawnTimeToConsumerIdsPrefix           = collections.NewPrefix(19)
	RemovalTimeToConsumerIdsPrefix         = collections.NewPrefix(20)
	ClientIdToConsumerIdPrefix             = collections.NewPrefix(21)
	ConsumerIdToDebtPrefix                 = collections.NewPrefix(22)
	InfractionParamsPrefix                 = collections.NewPrefix(23)
	ConsumerIdToFeesPerBlockOverridePrefix = collections.NewPrefix(24)
	ConsumerFeePoolSharesPrefix            = collections.NewPrefix(25)
	ConsumerFeePoolTotalSharesPrefix       = collections.NewPrefix(26)
	FeePoolAddressToConsumerIdPrefix       = collections.NewPrefix(27)
	EpochDowntimePrefix                    = collections.NewPrefix(28)
	ParametersPrefix                       = collections.NewPrefix(0xFF)
)
