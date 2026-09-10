package types

// Provider events
const (
	EventTypeConsumerClientCreated           = "consumer_client_created"
	EventTypeAssignConsumerKey               = "assign_consumer_key"
	EventTypeExecuteConsumerChainSlash       = "execute_consumer_chain_slash"
	EventTypeCreateConsumer                  = "create_consumer"
	EventTypeUpdateConsumer                  = "update_consumer"
	EventTypeRemoveConsumer                  = "remove_consumer"
	EventTypeSetConsumerFeesPerBlock         = "set_consumer_fees_per_block"
	EventTypeConsumerFeePoolFund             = "consumer_fee_pool_fund"
	EventTypeConsumerFeePoolWithdraw         = "consumer_fee_pool_withdraw"
	EventTypeConsumerFeePoolSweep            = "consumer_fee_pool_sweep"
	EventTypeConsumerRefusalSet              = "vaas_consumer_refusal_set"
	EventTypeConsumerRefusalThresholdReached = "vaas_consumer_refusal_threshold_reached"
	EventTypeEquivocationPunishmentQueued    = "vaas_equivocation_punishment_queued"
	EventTypeEquivocationPunishmentExecuted  = "vaas_equivocation_punishment_executed"
	EventTypeEquivocationPunishmentCancelled = "vaas_equivocation_punishment_cancelled"
	EventTypeEquivocationPunishmentDropped   = "vaas_equivocation_punishment_dropped"

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
	AttributeConsumerBinaryHash       = "consumer_binary_hash"
	AttributeConsumerGenesisHash      = "consumer_genesis_hash"
	AttributeKeyAmount                = "amount"
	AttributeDepositor                = "depositor"
	AttributeRecipient                = "recipient"
	AttributeAmount                   = "amount"
	AttributeDenom                    = "denom"
	AttributeTotalDistributed         = "total_distributed"
	AttributeDust                     = "dust"
	AttributeWithdrawPath             = "withdraw_path"
	AttributeRefused                  = "refused"
	AttributeRefusedPower             = "refused_power"
	AttributeTotalPower               = "total_power"
	AttributeRefusalThreshold         = "refusal_threshold"
	AttributeExecutesAt               = "executes_at"
	AttributeCancelReason             = "cancel_reason"

	// AttributeWithdrawPath values
	WithdrawPathDirect        = "direct"
	WithdrawPathCommunityPool = "community_pool"
)

const (
	// AttributeConsumerClientID carries the IBC client id declared for a
	// consumer via MsgUpdateConsumer.
	AttributeConsumerClientID = "consumer_client_id"
)
