package types

// Provider events
const (
	EventTypeConsumerClientCreated     = "consumer_client_created"
	EventTypeAssignConsumerKey         = "assign_consumer_key"
	EventTypeExecuteConsumerChainSlash = "execute_consumer_chain_slash"
	EventTypeOptIn                     = "opt_in"
	EventTypeOptOut                    = "opt_out"
	EventTypeCreateConsumer            = "create_consumer"
	EventTypeUpdateConsumer            = "update_consumer"
	EventTypeRemoveConsumer            = "remove_consumer"

	AttributeInfractionHeight         = "infraction_height"
	AttributeInitialHeight            = "initial_height"
	AttributeTrustingPeriod           = "trusting_period"
	AttributeUnbondingPeriod          = "unbonding_period"
	AttributeValsetHash               = "valset_hash"
	AttributeProviderValidatorAddress = "provider_validator_address"
	AttributeConsumerConsensusPubKey  = "consumer_consensus_pub_key"
	AttributeSubmitterAddress         = "submitter_address"
	AttributeConsumerId               = "consumer_id"
	AttributeConsumerChainId          = "consumer_chain_id"
	AttributeConsumerName             = "consumer_name"
	AttributeConsumerOwner            = "consumer_owner"
	AttributeConsumerSpawnTime        = "consumer_spawn_time"
	AttributeConsumerPhase            = "consumer_phase"
	AttributeConsumerTopN             = "consumer_topn"
)
