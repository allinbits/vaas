package types

// Provider events
const (
	EventTypeAssignConsumerKey       = "vaas_assign_consumer_key"
	EventTypeCreateConsumer          = "vaas_create_consumer"
	EventTypeUpdateConsumer          = "vaas_update_consumer"
	EventTypeRemoveConsumer          = "vaas_remove_consumer"
	EventTypeSetConsumerFeesPerBlock = "vaas_set_consumer_fees_per_block"
	EventTypeConsumerFeePoolFund     = "vaas_consumer_fee_pool_fund"
	EventTypeConsumerFeePoolWithdraw = "vaas_consumer_fee_pool_withdraw"
	EventTypeConsumerFeePoolSweep    = "vaas_consumer_fee_pool_sweep"

	AttributeProviderValidatorAddress = "provider_validator_address"
	// AttributeConsumerClientID carries the IBC client id declared for a
	// consumer; defined here so the wholesale rewrite of this block does not
	// drop it out from under its user. Harmless ahead of that user.
	AttributeConsumerClientID        = "consumer_client_id"
	AttributeConsumerConsensusPubKey = "consumer_consensus_pub_key"
	AttributeSubmitterAddress        = "submitter_address"
	AttributeConsumerId              = "consumer_id"
	AttributeConsumerChainId         = "consumer_chain_id"
	AttributeConsumerName            = "consumer_name"
	AttributeConsumerOwner           = "consumer_owner"
	AttributeConsumerSpawnTime       = "consumer_spawn_time"
	AttributeConsumerPhase           = "consumer_phase"
	AttributeConsumerBinaryHash      = "consumer_binary_hash"
	AttributeConsumerGenesisHash     = "consumer_genesis_hash"
	AttributeDepositor               = "depositor"
	AttributeRecipient               = "recipient"
	AttributeAmount                  = "amount"
	AttributeDenom                   = "denom"
	AttributeTotalDistributed        = "total_distributed"
	AttributeDust                    = "dust"
	AttributeWithdrawPath            = "withdraw_path"

	// AttributeWithdrawPath values
	WithdrawPathDirect        = "direct"
	WithdrawPathCommunityPool = "community_pool"
)
