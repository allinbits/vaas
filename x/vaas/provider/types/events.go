package types

// Provider events
const (
	EventTypeAssignConsumerKey               = "vaas_assign_consumer_key"
	EventTypeCreateConsumer                  = "vaas_create_consumer"
	EventTypeUpdateConsumer                  = "vaas_update_consumer"
	EventTypeRemoveConsumer                  = "vaas_remove_consumer"
	EventTypeSetConsumerFeesPerBlock         = "vaas_set_consumer_fees_per_block"
	EventTypeConsumerFeePoolFund             = "vaas_consumer_fee_pool_fund"
	EventTypeConsumerFeePoolWithdraw         = "vaas_consumer_fee_pool_withdraw"
	EventTypeConsumerFeePoolSweep            = "vaas_consumer_fee_pool_sweep"
	EventTypeConsumerRefusalSet              = "vaas_consumer_refusal_set"
	EventTypeConsumerRefusalThresholdReached = "vaas_consumer_refusal_threshold_reached"
	EventTypeEquivocationPunishmentQueued    = "vaas_equivocation_punishment_queued"
	EventTypeEquivocationPunishmentExecuted  = "vaas_equivocation_punishment_executed"
	EventTypeEquivocationPunishmentCancelled = "vaas_equivocation_punishment_cancelled"
	EventTypeEquivocationPunishmentDropped   = "vaas_equivocation_punishment_dropped"
	// EventTypeConsumerClientChainIdMismatch is emitted once, alongside
	// vaas_consumer_paused, when the provider BeginBlock pauses a launched
	// consumer whose declared client tracks a chain id other than the
	// consumer's (PauseConsumersWithMismatchedClients): an ibc-go client
	// upgrade or recovery switched the client's chain id. Governance resumes
	// the consumer after renaming it to the client's chain id
	// (MsgUpdateConsumer by the authority) or recovering the client onto the
	// consumer's.
	EventTypeConsumerClientChainIdMismatch = "vaas_consumer_client_chain_id_mismatch"

	AttributeProviderValidatorAddress = "provider_validator_address"
	// AttributeConsumerClientID carries the IBC client id declared for a
	// consumer via MsgUpdateConsumer.
	AttributeConsumerClientID = "consumer_client_id"
	// AttributeClientChainId carries the chain id a declared client tracks.
	AttributeClientChainId = "client_chain_id"
	// AttributePreviousChainId carries the chain id a renamed consumer had.
	AttributePreviousChainId = "previous_chain_id"
	// AttributePauseReason names, on the consumer_paused event, the
	// PauseReason the consumer was paused for.
	AttributePauseReason             = "reason"
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
	AttributeRefused                 = "refused"
	AttributeRefusedPower            = "refused_power"
	AttributeTotalPower              = "total_power"
	AttributeRefusalThreshold        = "refusal_threshold"
	AttributeExecutesAt              = "executes_at"
	AttributeCancelReason            = "cancel_reason"

	// AttributeWithdrawPath values
	WithdrawPathDirect        = "direct"
	WithdrawPathCommunityPool = "community_pool"
)
