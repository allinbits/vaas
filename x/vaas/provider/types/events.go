package types

// Provider events
const (
	EventTypeAssignConsumerKey       = "assign_consumer_key"
	EventTypeCreateConsumer          = "create_consumer"
	EventTypeUpdateConsumer          = "update_consumer"
	EventTypeRemoveConsumer          = "remove_consumer"
	EventTypeSetConsumerFeesPerBlock = "set_consumer_fees_per_block"
	EventTypeConsumerFeePoolFund     = "consumer_fee_pool_fund"
	EventTypeConsumerFeePoolWithdraw = "consumer_fee_pool_withdraw"
	EventTypeConsumerFeePoolSweep    = "consumer_fee_pool_sweep"

	AttributeProviderValidatorAddress = "provider_validator_address"
	AttributeConsumerConsensusPubKey  = "consumer_consensus_pub_key"
	AttributeSubmitterAddress         = "submitter_address"
	AttributeConsumerId               = "consumer_id"
	AttributeConsumerChainId          = "consumer_chain_id"
	AttributeConsumerName             = "consumer_name"
	AttributeConsumerOwner            = "consumer_owner"
	AttributeConsumerSpawnTime        = "consumer_spawn_time"
	AttributeConsumerPhase            = "consumer_phase"
	AttributeConsumerBinaryHash       = "consumer_binary_hash"
	AttributeConsumerGenesisHash      = "consumer_genesis_hash"
	AttributeDepositor                = "depositor"
	AttributeRecipient                = "recipient"
	AttributeAmount                   = "amount"
	AttributeDenom                    = "denom"
	AttributeTotalDistributed         = "total_distributed"
	AttributeDust                     = "dust"
	AttributeWithdrawPath             = "withdraw_path"

	// AttributeWithdrawPath values
	WithdrawPathDirect        = "direct"
	WithdrawPathCommunityPool = "community_pool"
)
