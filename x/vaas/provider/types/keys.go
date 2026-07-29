package types

import (
	"cosmossdk.io/collections"
)

const (
	// ModuleName defines the VAAS provider module name
	ModuleName = "provider"

	// StoreKey is the store key string for IBC provider
	StoreKey = ModuleName

	// Default validator set update ID
	DefaultValsetUpdateID = 1

	// Names for the collection storage keys.
	ConsumerIdToFeesPerBlockOverrideKeyName = "ConsumerIdToFeesPerBlockOverrideKey"

	ConsumerFeePoolSharesKeyName      = "ConsumerFeePoolSharesKey"
	ConsumerFeePoolTotalSharesKeyName = "ConsumerFeePoolTotalSharesKey"
	FeePoolAddressToConsumerIdKeyName = "FeePoolAddressToConsumerIdKey"

	EpochDowntimeKeyName = "EpochDowntimeKey"

	PreviousDowntimeParamsKeyName = "PreviousDowntimeParamsKey"

	EpochShareRecordsKeyName = "EpochShareRecordsKey"

	PendingDowntimeSlashesKeyName = "PendingDowntimeSlashesKey"

	AcceptedDowntimeWindowsKeyName = "AcceptedDowntimeWindowsKey"

	WithheldFeeRecordsKeyName = "WithheldFeeRecordsKey"

	ConsumerIdToPauseExpirationTimeKeyName = "ConsumerIdToPauseExpirationTimeKey"

	PauseExpirationTimeToConsumerIdsKeyName = "PauseExpirationTimeToConsumerIdsKey"

	DowntimeWindowFloorsKeyName = "DowntimeWindowFloorsKey"
)

// Collection key prefixes for use with cosmossdk.io/collections
var (
	ValidatorSetUpdateIdPrefix = collections.NewPrefix(0)
	// ConsumerIdToClientIdPrefix holds the mapping from consumer ID to client ID.
	// This is the primary lookup mechanism for IBC v2 client-based communication.
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
	// ClientIdToConsumerIdPrefix holds the reverse mapping from client ID to
	// consumer ID, backing the reverse lookup for IBC v2 client-based
	// communication.
	ClientIdToConsumerIdPrefix             = collections.NewPrefix(21)
	ConsumerIdToDebtPrefix                 = collections.NewPrefix(22)
	InfractionParamsPrefix                 = collections.NewPrefix(23)
	ConsumerIdToFeesPerBlockOverridePrefix = collections.NewPrefix(24)
	ConsumerFeePoolSharesPrefix            = collections.NewPrefix(25)
	ConsumerFeePoolTotalSharesPrefix       = collections.NewPrefix(26)
	FeePoolAddressToConsumerIdPrefix       = collections.NewPrefix(27)
	EpochDowntimePrefix                    = collections.NewPrefix(28)
	ConsumerIdToLastAckTimePrefix          = collections.NewPrefix(29)
	ConsumerIdToHighestSentVscIdPrefix     = collections.NewPrefix(30)
	ConsumerIdToHighestAckedVscIdPrefix    = collections.NewPrefix(31)
	EpochShareRecordsPrefix                = collections.NewPrefix(32)
	PendingDowntimeSlashesPrefix           = collections.NewPrefix(33)
	PreviousDowntimeParamsPrefix           = collections.NewPrefix(34)
	AcceptedDowntimeWindowsPrefix          = collections.NewPrefix(35)
	WithheldFeeRecordsPrefix               = collections.NewPrefix(36)
	ConsumerIdToPauseExpirationTimePrefix  = collections.NewPrefix(37)
	PauseExpirationTimeToConsumerIdsPrefix = collections.NewPrefix(38)
	DowntimeWindowFloorsPrefix             = collections.NewPrefix(39)
	ParametersPrefix                       = collections.NewPrefix(0xFF)
)
