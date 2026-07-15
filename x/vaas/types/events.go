package types

// VAAS events
const (
	EventTypeTimeout                    = "timeout"
	EventTypePacket                     = "vaas_packet"
	EventTypeChannelEstablished         = "channel_established"
	EventTypeConsumerClientCreated      = "consumer_client_created"
	EventTypeAssignConsumerKey          = "assign_consumer_key"
	EventTypeSubmitConsumerMisbehaviour = "submit_consumer_misbehaviour"
	EventTypeSubmitConsumerDoubleVoting = "submit_consumer_double_voting"
	EventTypeExecuteConsumerChainSlash  = "execute_consumer_chain_slash"
	EventTypeConsumerEvidenceRequest    = "consumer_evidence_request"
	// EventTypeSnapshotResync is emitted by the consumer when it applies a
	// snapshot VSC packet (is_snapshot=true), i.e. it replaces its cross-chain
	// validator set rather than accumulating a diff. Emitted only on snapshots,
	// not on ordinary diffs.
	EventTypeSnapshotResync = "vaas_snapshot_resync"
	// EventTypePendingDowntimeSlash is emitted by the provider when downtime
	// evidence is accepted and queued behind the downtime challenge window
	// (see HandleConsumerDowntime). It does not mean a slash executed --
	// execution happens later, once the entry matures, in the epoch sweep.
	EventTypePendingDowntimeSlash = "vaas_pending_downtime_slash"
	// EventTypeDowntimeSlashDropped is emitted by the provider's epoch sweep
	// (SweepPendingDowntimeSlashes) when a matured PendingDowntimeSlash entry
	// is discarded instead of executed -- e.g. the validator has since
	// unbonded, been tombstoned, or vanished, or the entry has a zero token
	// amount. The entry is deleted either way; this event carries the reason.
	EventTypeDowntimeSlashDropped = "vaas_downtime_slash_dropped"
	// EventTypeDowntimeChallengeSucceeded is emitted by the provider when a
	// MsgChallengeConsumerDowntime successfully proves the validator signed
	// the claimed height, cancelling the pending downtime slash.
	EventTypeDowntimeChallengeSucceeded = "vaas_downtime_challenge_succeeded"
	// EventTypeConsumerPaused is emitted when a consumer chain transitions
	// into CONSUMER_PHASE_PAUSED following a successful downtime challenge.
	EventTypeConsumerPaused = "vaas_consumer_paused"
	// EventTypeConsumerResumed is emitted when a paused consumer chain is
	// resumed via MsgResumeConsumer.
	EventTypeConsumerResumed = "vaas_consumer_resumed"

	AttributeKeyAckSuccess            = "success"
	AttributeKeyAck                   = "acknowledgement"
	AttributeKeyAckError              = "error"
	AttributeInfractionHeight         = "infraction_height"
	AttributeConsumerHeight           = "consumer_height"
	AttributeTimestamp                = "timestamp"
	AttributeInitialHeight            = "initial_height"
	AttributeInitializationTimeout    = "initialization_timeout"
	AttributeTrustingPeriod           = "trusting_period"
	AttributeUnbondingPeriod          = "unbonding_period"
	AttributeProviderValidatorAddress = "provider_validator_address"
	AttributeConsumerConsensusPubKey  = "consumer_consensus_pub_key"
	AttributeConsumerMisbehaviour     = "consumer_misbehaviour"
	AttributeMisbehaviourClientId     = "misbehaviour_client_id"
	AttributeMisbehaviourHeight1      = "misbehaviour_height_1"
	AttributeMisbehaviourHeight2      = "misbehaviour_height_2"
	AttributeByzantineValidators      = "byzantine_validators"
	AttributeConsumerDoubleVoting     = "consumer_double_voting"
	AttributeChainID                  = "chain_id"
	AttributeValidatorAddress         = "validator_address"
	AttributeInfractionType           = "infraction_type"
	AttributeValSetUpdateID           = "valset_update_id"
	AttributeNumValidators            = "num_validators"
	AttributeWindowStartHeight        = "window_start_height"
	AttributeMissedCount              = "missed_count"
	AttributeMissedBlocksBitmap       = "missed_blocks_bitmap"
	AttributeSlashTokens              = "slash_tokens"
	AttributeMaturesAt                = "matures_at"
	AttributeDropReason               = "reason"
	AttributeChallenger               = "challenger"
	AttributeClaimedHeight            = "claimed_height"
)
