package types

const (
	// ModuleName defines the VAAS module name
	ModuleName = "VAAS"

	// Version defines the current version the IBC VAAS provider and consumer
	// module supports. Used for IBC v1 channel version negotiation and
	// IBC v2 payload version field.
	Version = "1"

	// ProviderPortID is the default port id the provider VAAS module binds to.
	// Deprecated: ProviderPortID is used for IBC v1 channel-based communication.
	// For IBC v2, use ProviderAppID instead.
	ProviderPortID = "provider"

	// ConsumerPortID is the default port id the consumer VAAS module binds to.
	// Deprecated: ConsumerPortID is used for IBC v1 channel-based communication.
	// For IBC v2, use ConsumerAppID instead.
	ConsumerPortID = "consumer"

	// ProviderAppID is the application identifier for the provider in IBC v2.
	// This replaces port-based routing with client-based routing.
	ProviderAppID = "vaas/provider"

	// ConsumerAppID is the application identifier for the consumer in IBC v2.
	// This replaces port-based routing with client-based routing.
	ConsumerAppID = "vaas/consumer"

	RouterKey = ModuleName

	// StoreKey defines the primary module store key
	StoreKey = ModuleName

	// MemStoreKey defines the in-memory store key
	MemStoreKey = "mem_vaas"
)
