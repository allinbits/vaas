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
)
