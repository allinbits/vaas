# Events Reference

Every event VAAS emits, what triggers it, and what it carries. This is the list
to build an indexer, an alerting rule, or a validator dashboard against.

All VAAS events are legacy string events (`ctx.EventManager().EmitEvent`) -- there
are no typed protobuf events, so there is no `Event` message in `proto/` to
generate clients from. Match on the event type string.

Two conventions worth knowing before you filter:

- **Most events carry a `module` attribute** (`vaasprovider` or `vaasconsumer`),
  but the three fee-pool events do **not**. An indexer that filters on
  `module = "vaasprovider"` silently misses fund, withdraw, and sweep.
- **`vaas_packet` and `vaas_timeout` are emitted by both sides** with different
  attribute sets. Disambiguate on `module`.

---

## Provider events

| Event type | Emitted by | Attributes | Meaning |
|---|---|---|---|
| `vaas_create_consumer` | `msgServer.CreateConsumer` | `module`, `consumer_id`, `consumer_chain_id`, `consumer_name`, `submitter_address`, `consumer_owner`, `consumer_phase`; plus `consumer_spawn_time`, `consumer_binary_hash`, `consumer_genesis_hash` when set | A consumer was registered. `consumer_phase` is `REGISTERED` or, if a spawn time was given, `INITIALIZED`. |
| `vaas_update_consumer` | `msgServer.UpdateConsumer` | `module`, `consumer_id`, `consumer_chain_id`, `submitter_address`, `consumer_owner`, `consumer_phase`; plus `consumer_name` when metadata changed and `consumer_spawn_time` / `consumer_binary_hash` / `consumer_genesis_hash` when initialization parameters changed | Metadata, initialization parameters, or ownership changed. |
| `vaas_remove_consumer` | `msgServer.RemoveConsumer` | `module`, `consumer_id`, `consumer_chain_id`, `submitter_address` | Governance stopped a `LAUNCHED` or `PAUSED` consumer. It is now `STOPPED` and queued for deletion. |
| `vaas_retire_consumer` | `msgServer.RetireConsumer` | `module`, `consumer_id`, `consumer_chain_id`, `submitter_address` | The owner or governance terminated a consumer that had not launched. It is now `DELETED`, its fee pool has been swept, and `consumer_chain_id` is the chain ID the deletion just released -- read it here, since `consumer-chain` reports it empty afterwards. |
| `vaas_consumer_paused` | `Keeper.PauseConsumerChain` | `module`, `consumer_id` | The consumer moved `LAUNCHED` to `PAUSED`. Only a successful downtime challenge does this. |
| `vaas_consumer_resumed` | `Keeper.ResumeConsumerChain` | `module`, `consumer_id` | Governance resumed a paused consumer; the snapshot VSC has already been sent. |
| `vaas_assign_consumer_key` | `msgServer.AssignConsumerKey` | `module`, `consumer_id`, `consumer_chain_id`, `provider_validator_address`, `consumer_consensus_pub_key`, `submitter_address` | A validator assigned a per-consumer consensus key. |
| `vaas_submit_consumer_double_voting` | `msgServer.SubmitConsumerDoubleVoting` | `module`, `consumer_id`, `consumer_chain_id`, `consumer_double_voting`, `submitter_address` | Duplicate-vote evidence was accepted; the offender was slashed, jailed, and tombstoned. |
| `vaas_submit_consumer_misbehaviour` | `msgServer.SubmitConsumerMisbehaviour` | `module`, `consumer_id`, `consumer_chain_id`, `consumer_misbehaviour`, `misbehaviour_client_id`, `misbehaviour_height_1`, `misbehaviour_height_2`, `byzantine_validators`, `submitter_address` | Light-client misbehaviour was accepted. `byzantine_validators` is a comma-joined list, and is empty when nobody could be punished -- in which case the consumer was stopped terminally (see [equivocation-evidence.md](equivocation-evidence.md)). |
| `vaas_pending_downtime_slash` | `Keeper.HandleConsumerDowntime` | `module`, `consumer_id`, `provider_validator_address`, `window_start_height`, `window_end_height`, `missed_count`, `missed_blocks_bitmap` (hex), `slash_tokens`, `matures_at` | **Downtime evidence accepted.** The slash is priced and queued behind the challenge window, *not* executed. This is the event an accused validator watches: the bitmap says which heights to disprove and `matures_at` is the deadline. |
| `vaas_execute_consumer_chain_slash` | `Keeper.executeDowntimeSlash`, from the provider `BeginBlock` sweep | `module`, `consumer_id`, `provider_validator_address`, `infraction_type`, `slash_tokens` | A matured pending downtime slash actually executed. |
| `vaas_downtime_slash_dropped` | `Keeper.emitDowntimeSlashDropped`, same sweep | `module`, `consumer_id`, `provider_validator_address`, `reason` | A matured entry was discarded instead of executed -- zero slash amount, no slashable stake, or an error. Check `reason`. |
| `vaas_downtime_challenge_succeeded` | `Keeper.HandleChallengeConsumerDowntime` | `module`, `consumer_id`, `challenger`, `provider_validator_address`, `claimed_height` | A challenge proved the validator signed the claimed height. Every pending slash from this consumer is cancelled; `vaas_withheld_fee_paid` and `vaas_consumer_paused` accompany it. |
| `vaas_withheld_fee_paid` | `Keeper.PayWithheldFees` | `module`, `consumer_id`, `provider_validator_address`, `amount` | One withheld fee record was repaid after a successful challenge. Emitted once per record. |
| `vaas_set_consumer_fees_per_block` | `msgServer.SetConsumerFeesPerBlock` | `module`, `consumer_id`, `amount` | A per-consumer fee-per-block override was set. An empty `amount` means the override was cleared. |
| `vaas_consumer_fee_pool_fund` | `msgServer.FundConsumerFeePool` | `consumer_id`, `depositor`, `amount` -- **no `module`** | A deposit landed and shares were minted. |
| `vaas_consumer_fee_pool_withdraw` | `msgServer.WithdrawConsumerFeePool` | `consumer_id`, `depositor`, `recipient`, `amount`, `withdraw_path` -- **no `module`** | Shares burned, tokens returned. `withdraw_path` is `direct` or `community_pool` (the gov clawback). On the gov path `depositor` and `recipient` are the same distribution module address, which is what `withdraw_path` exists to disambiguate. |
| `vaas_consumer_fee_pool_sweep` | `Keeper.emitSweepEvent` | `consumer_id`, `denom`, `total_distributed`, `dust` -- **no `module`** | One event **per swept denom**, from either `MsgSweepConsumerFeePool` or the auto-sweep on consumer deletion. `dust` is the truncation residue forwarded to the community pool. |
| `vaas_packet` | provider `IBCModule.OnAcknowledgementPacket` | `module` (`vaasprovider`), `source_client`, `sequence` | A VSC packet was acknowledged by a consumer. The ack status is **not** in the attributes -- an error ack looks the same as a success ack here. |
| `vaas_timeout` | provider `IBCModule.OnTimeoutPacket` | `module` (`vaasprovider`), `source_client`, `sequence` | A provider-sent VSC packet timed out. |

## Consumer events

| Event type | Emitted by | Attributes | Meaning |
|---|---|---|---|
| `vaas_consumer_evidence_request` | `Keeper.SendEvidencePackets`, in the consumer `EndBlock` | `module` (`vaasconsumer`), `validator_address`, `window_end_height`, `infraction_type` | A downtime evidence packet was handed to IBC and dequeued. One per packet sent. |
| `vaas_consumer_evidence_rejected` | `Keeper.DropRejectedEvidencePacket`, from `OnAcknowledgementPacket` | `module` (`vaasconsumer`), `validator_address`, `window_end_height`, `error` (hex-encoded ack bytes) | The provider error-acked the evidence, which is always permanent for that packet, so it is dropped rather than retried. **This is the only signal that evidence was refused** -- the provider emits nothing on a rejection. On an undecodable payload `validator_address` is empty and `window_end_height` is `0`. |
| `vaas_snapshot_resync` | `Keeper.OnRecvVSCPacketV2` | `module` (`vaasconsumer`), `valset_update_id`, `num_validators` | A snapshot VSC replaced the whole validator set. Not emitted for ordinary diffs. |
| `vaas_client_established` | `Keeper.enforcePinnedProviderClient` | `module` (`vaasconsumer`), `client_id` | The consumer re-pinned its provider client from the unroutable genesis client to the client that actually delivered a VSC. One-shot: it never fires again for that chain. |
| `vaas_packet` | consumer `IBCModule.OnRecvPacket` | `module` (`vaasconsumer`), `valset_update_id`, `success`, `source_client` | A VSC packet was received and applied. `success` is always `true` here -- a rejected packet returns an error acknowledgement and emits nothing. |
| `vaas_timeout` | consumer `IBCModule.OnTimeoutPacket` | `module` (`vaasconsumer`), `source_client`, `sequence` | A consumer-sent evidence packet timed out. Its payload was re-queued for retry, unlike a rejection. |

---

## What is deliberately not an event

Several paths log only, so do not build alerting on events that do not exist:

- **Consumer launch, auto-stop, and deletion.** `LaunchConsumer`,
  `StopAndPrepareForConsumerRemoval`, `DeleteConsumerChain`, the liveness sweep,
  and the pause auto-stop emit nothing. Only the *messages* that drive
  lifecycle changes emit (`vaas_create_consumer`, `vaas_update_consumer`,
  `vaas_remove_consumer`, `vaas_retire_consumer`, `vaas_consumer_paused`,
  `vaas_consumer_resumed`). `vaas_retire_consumer` is the one exception that does
  report a deletion, because a pre-launch retirement *is* a message.
  Watch the `phase` field of `consumer-chain` / `list-consumer-chains` instead
  (see [queries-reference.md](queries-reference.md)).
- **Downtime evidence rejection on the provider.** Rejections are returned as
  IBC error acknowledgements; the consumer surfaces them as
  `vaas_consumer_evidence_rejected`.
- **Client discovery and look-alike rejection on the provider.**
  `discoverActiveConsumerClient` logs at info and warn level only. The only
  client-related event is the consumer-side `vaas_client_established`.
- **Epoch fee distribution.** `DistributeConsumerFees` emits nothing; only the
  fund, withdraw, and sweep messages do.
- **Debt status changes.** `ConsumerInDebt` is state and a VSC packet field, not
  an event.
- **Safe mode entry and exit** on the consumer.
- **Withheld-fee expiry.** A record that expires unchallenged is deleted
  silently, unlike a record that is paid out (`vaas_withheld_fee_paid`).
