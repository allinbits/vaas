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
| `vaas_remove_consumer` | `msgServer.RemoveConsumer` | `module`, `consumer_id`, `consumer_chain_id`, `consumer_phase`, `submitter_address` | A consumer was removed; `consumer_phase` is the phase at removal and tells the arms apart. Pre-launch phases mean the owner or governance erased it immediately: it is now `DELETED`, its fee pool swept, and `consumer_chain_id` is the chain ID the deletion just released. `LAUNCHED` or `PAUSED` mean governance stopped it: it is now `STOPPED` and queued for deletion after the unbonding period, and one `vaas_equivocation_punishment_cancelled` fires per pending punishment the chain had sourced. |
| `vaas_consumer_paused` | `Keeper.PauseConsumerChain` | `module`, `consumer_id` | The consumer moved `LAUNCHED` to `PAUSED`, by a successful downtime challenge or by the bonded power refusing it reaching `refusal_pause_threshold` (`vaas_consumer_refusal_threshold_reached` fires alongside in that case). |
| `vaas_consumer_resumed` | `Keeper.ResumeConsumerChain` | `module`, `consumer_id` | Governance resumed a paused consumer; the snapshot VSC has already been sent. |
| `vaas_assign_consumer_key` | `msgServer.AssignConsumerKey` | `module`, `consumer_id`, `consumer_chain_id`, `provider_validator_address`, `consumer_consensus_pub_key`, `submitter_address` | A validator assigned a per-consumer consensus key. |
| `vaas_submit_consumer_double_voting` | `msgServer.SubmitConsumerDoubleVoting` | `module`, `consumer_id`, `consumer_chain_id`, `consumer_double_voting`, `submitter_address` | Duplicate-vote evidence was accepted; the offender is jailed and its punishment queued (`vaas_equivocation_punishment_queued` fires alongside). The slash and tombstone come later, see below. |
| `vaas_consumer_refusal_set` | `msgServer.SetConsumerRefusal` | `module`, `consumer_id`, `provider_validator_address`, `refused` | A validator recorded (`true`) or withdrew (`false`) its refusal to validate the consumer. The pause itself is evaluated in the provider `EndBlock`. |
| `vaas_consumer_refusal_threshold_reached` | `Keeper.EvaluateConsumerRefusals`, provider `EndBlock` | `module`, `consumer_id`, `refused_power`, `total_power`, `refusal_threshold` | The refused share of bonded power reached the threshold and the consumer was paused; `vaas_consumer_paused` accompanies it. |
| `vaas_equivocation_punishment_queued` | `Keeper.QueuePendingEquivocationPunishment`, from `HandleConsumerDoubleVoting` | `module`, `consumer_id`, `provider_validator_address`, `executes_at` | Double-vote evidence verified: the validator is jailed and its unbonding operations held; slash and tombstone wait until `executes_at`. |
| `vaas_equivocation_punishment_executed` | `Keeper.executePendingEquivocation`, from the provider `BeginBlock` sweep (`SweepPendingEquivocationPunishments`) | `module`, `consumer_id`, `provider_validator_address` | The deferred slash and tombstone executed. |
| `vaas_equivocation_punishment_cancelled` | `Keeper.cancelPendingEquivocation`, from `msgServer.RemoveConsumer` or the sweep | `module`, `consumer_id`, `provider_validator_address`, `cancel_reason` | Governance condemned the consumer (the removal executed, or the removal vote the punishment deferred behind passed): the punishment is dropped, holds released, the jail opened. |
| `vaas_equivocation_punishment_dropped` | `Keeper.dropPendingEquivocation`, same sweep | `module`, `consumer_id`, `provider_validator_address`, `reason` | A punishment that can no longer execute was discarded: its validator is already tombstoned or no longer exists. Check `reason`. Not a verdict on the evidence. |
| `vaas_submit_consumer_misbehaviour` | `msgServer.SubmitConsumerMisbehaviour` | `module`, `consumer_id`, `consumer_chain_id`, `consumer_misbehaviour`, `misbehaviour_client_id`, `misbehaviour_height_1`, `misbehaviour_height_2`, `byzantine_validators`, `submitter_address` | Light-client misbehaviour was verified and the consumer was paused; `vaas_consumer_paused` fires alongside. Nobody is punished. `byzantine_validators` is a comma-joined, informational attribution, empty for an amnesia attack (see [equivocation-evidence.md](equivocation-evidence.md)). |
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
| `vaas_consumer_evidence_rejected` | `Keeper.ReportRejectedEvidencePacket`, from `OnAcknowledgementPacket` | `module` (`vaasconsumer`), `validator_address`, `window_end_height` | The provider error-acked the evidence, which is always permanent for that packet, so it is not retried. **This is the only signal that evidence was refused** -- the provider emits nothing on a rejection. The ack bytes are not carried: an IBC v2 error acknowledgement is a sentinel constant with no application error. On an undecodable payload `validator_address` is empty and `window_end_height` is `0`. |
| `vaas_snapshot_resync` | `Keeper.OnRecvVSCPacketV2` | `module` (`vaasconsumer`), `valset_update_id`, `num_validators` | A snapshot VSC replaced the whole validator set. Not emitted for ordinary diffs. |
| `vaas_client_established` | `msgServer.SetProviderClient` | `module` (`vaasconsumer`), `client_id` | The consumer's owner (or governance) declared the provider client, setting the pin. One-shot: the pin is permanent, so it never fires again for that chain. |
| `vaas_packet` | consumer `IBCModule.OnRecvPacket` | `module` (`vaasconsumer`), `valset_update_id`, `success`, `source_client` | A VSC packet was received and applied. `success` is always `true` here -- a rejected packet returns an error acknowledgement and emits nothing. |
| `vaas_timeout` | consumer `IBCModule.OnTimeoutPacket` | `module` (`vaasconsumer`), `source_client`, `sequence` | A consumer-sent evidence packet timed out. Its payload was re-queued for retry, unlike a rejection. |

---

## What is deliberately not an event

Several paths log only, so do not build alerting on events that do not exist:

- **Consumer launch, auto-stop, and deletion.** `LaunchConsumer`,
  `StopAndPrepareForConsumerRemoval`, `DeleteConsumerChain`, the liveness sweep,
  and the pause auto-stop emit nothing. Only the *messages* that drive
  lifecycle changes emit (`vaas_create_consumer`, `vaas_update_consumer`,
  `vaas_remove_consumer`, `vaas_consumer_paused`, `vaas_consumer_resumed`).
  The one deletion an event does report is `vaas_remove_consumer` with a
  pre-launch `consumer_phase`, because that immediate erasure *is* a message.
  Watch the `phase` field of `consumer-chain` / `list-consumer-chains` instead
  (see [queries-reference.md](queries-reference.md)).
- **Punishment deferral.** A pending downtime slash or equivocation punishment
  pushed past a removal vote emits nothing; the deferral shows in
  `pending-downtime-slashes` (`matures_at_extended`, `deferred_by_proposal_id`)
  and `pending-equivocation-punishments` (`executes_at_extended`,
  `deferred_by_proposal_id`).
- **Downtime evidence rejection on the provider.** Rejections are returned as
  IBC error acknowledgements; the consumer surfaces them as
  `vaas_consumer_evidence_rejected`.
- **Client declaration on the provider.** The binding set through
  `MsgUpdateConsumer`'s `client_id` rides that message's ordinary
  `vaas_update_consumer` event. The only dedicated client event is the
  consumer-side `vaas_client_established`.
- **Epoch fee distribution.** `DistributeConsumerFees` emits nothing; only the
  fund, withdraw, and sweep messages do.
- **Debt status changes.** `ConsumerInDebt` is state and a VSC packet field, not
  an event.
- **Safe mode entry and exit** on the consumer.
- **Withheld-fee expiry.** A record that expires unchallenged is deleted
  silently, unlike a record that is paid out (`vaas_withheld_fee_paid`).
