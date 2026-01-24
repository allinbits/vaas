package types

const (
	// ModuleName defines the VAAS module name
	ModuleName = "VAAS"

	// Version defines the current version the IBC VAAS provider and consumer
	// module supports
	Version = "1"

	// ProviderPortID is the default port id the provider VAAS module binds to
	ProviderPortID = "provider"

	// ConsumerPortID is the default port id the consumer VAAS module binds to
	ConsumerPortID = "consumer"

	RouterKey = ModuleName

	// StoreKey defines the primary module store key
	StoreKey = ModuleName

	// MemStoreKey defines the in-memory store key
	MemStoreKey = "mem_vaas"
)
