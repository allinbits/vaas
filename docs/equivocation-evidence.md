# Equivocation and Light-Client Evidence

A consumer chain is secured by the provider's validators, but the provider does
not watch consumer consensus directly. Instead, anyone can submit evidence of
validator misconduct on a consumer to the provider, which verifies it and (for
double-signing) punishes the offender on the provider chain.

Two evidence types exist and differ in how the evidence is verified and in what
follows: a double-sign names an offender, who is jailed at once and whose slash
and tombstone are queued behind a delay governance can act within; a
light-client attack names nobody the provider can trust the evidence about, so
the chain is paused instead:

| Evidence | Message | Consequence |
|---|---|---|
| Double-sign (duplicate vote) | `MsgSubmitConsumerDoubleVoting` | jail now, unbonding operations held; slash + tombstone after `equivocation_execution_delay`, deferred once behind a live removal vote; cancelled if governance removes the consumer |
| Light-client attack (IBC misbehaviour) | `MsgSubmitConsumerMisbehaviour` | the consumer is paused; nobody is punished; the byzantine set is attributed on the event |

Both messages are permissionless -- any account can submit them as an ordinary
provider transaction. This is distinct from downtime, which flows automatically
as IBC evidence packets; see [consumer-downtime.md](consumer-downtime.md).

---

## Double-voting (duplicate vote)

A validator that signs two conflicting votes at the same height/round/type on a
consumer chain has equivocated. The evidence is a CometBFT
`DuplicateVoteEvidence` plus the IBC light-client header for the infraction
height (used to verify the evidence against the provider's client for that
consumer).

### Submit

```
providerd tx vaasprovider submit-consumer-double-voting \
    <consumer-id> <path/to/evidence.json> <path/to/infraction_header.json> \
    --from <account>
```

- `evidence.json` is a `cometbft/proto/tendermint/types` `DuplicateVoteEvidence`.
- `infraction_header.json` is an ibc-go `07-tendermint` `Header` for the
  infraction height.

Both files are decoded with the proto-JSON codec. CLI source:
`NewSubmitConsumerDoubleVotingCmd` in
[x/vaas/provider/client/cli/tx.go](../x/vaas/provider/client/cli/tx.go).

### What the provider does

`HandleConsumerDoubleVoting` in
[x/vaas/provider/keeper/consumer_equivocation.go](../x/vaas/provider/keeper/consumer_equivocation.go):

1. Requires the consumer to be `LAUNCHED`.
2. Rejects evidence older than the consumer's equivocation-evidence minimum
   height. Note the *age* of the vote is not otherwise bounded -- there is no
   max-age on double-vote evidence.
3. Verifies the evidence with `VerifyDoubleVotingEvidence`: the
   supplied public key's address matches the vote's validator address; the two
   votes share height/round/type and validator address but differ in block id;
   and both signatures verify against the consumer chain id.
4. Resolves the offender's provider consensus address (honouring key assignment,
   see [key-assignment.md](key-assignment.md)) and queues the punishment
   (`QueuePendingEquivocationPunishment`): the validator is **jailed** at once
   and its unbonding operations are **held**, while the **slash** (default 5%)
   and **tombstone** at `InfractionParameters.DoubleSign` execute
   `equivocation_execution_delay` later (default 7 days) in the provider
   `BeginBlock`, deferred once past a removal vote for the consumer that is in
   its voting period at that point. A passed removal cancels the punishment,
   releases the holds and opens the jail; a rejected one lets it execute. See
   [consumer-refusal.md](consumer-refusal.md) section 3. Repeated submissions
   of already-processed evidence are idempotent (already queued is a no-op,
   already-tombstoned is not an error).

On success the provider emits `vaas_submit_consumer_double_voting` and
`vaas_equivocation_punishment_queued`; the resolution later emits
`vaas_equivocation_punishment_executed` or `_cancelled`; see
[events-reference.md](events-reference.md).

The slash fraction, jail duration, and tombstone flag are the global infraction
parameters (see [params-reference.md](params-reference.md) section 2); with the
defaults, a double-signer whose consumer governance does not remove is slashed
5% and permanently removed seven days after the evidence is accepted.

---

## Light-client attack (IBC misbehaviour)

A light-client attack is two validly-signed but conflicting consumer headers at
the same height (an equivocation by 1/3+ of the consumer's voting power, or an
amnesia attack). The evidence is an ibc-go `07-tendermint` `Misbehaviour`
carrying the two headers.

### Submit

```
providerd tx vaasprovider submit-consumer-misbehaviour \
    <consumer-id> <path/to/misbehaviour.json> \
    --from <account>
```

`misbehaviour.json` is an ibc-go `Misbehaviour` (two conflicting client
headers). CLI source: `NewSubmitConsumerMisbehaviourCmd` in
[tx.go](../x/vaas/provider/client/cli/tx.go).

### Verification and containment

`HandleConsumerMisbehaviour`
([consumer_equivocation.go](../x/vaas/provider/keeper/consumer_equivocation.go))
verifies a light-client attack and contains it. No validator is punished on
this path:

1. Evidence for a consumer that is not `LAUNCHED` is rejected up front, so
   re-submitting evidence against an already-paused consumer is a cheap no-op.
2. `CheckMisbehaviour` verifies the chain id and client id match the consumer,
   that the two headers are at the same height and within the client trusting
   period, and that they genuinely conflict (different block id hashes, each
   valid against its trusted consensus state).
3. `GetByzantineValidators` extracts the validators that signed both
   conflicting headers. The attribution is informational only: it is logged and
   carried on the submission event so operators and governance can see who
   signed what. An amnesia attack has no attributable set by construction and
   the extraction returns none; containment applies all the same.
4. The consumer is **paused** (`containLightClientAttack` calling
   `PauseConsumerChain`): VSC service stops, both evidence paths reject the
   consumer (they gate on `LAUNCHED`), its pending downtime accusations are
   cancelled, and an auto-stop is scheduled at `MaxPauseDuration` so governance
   silence converges to `STOPPED`.

Nobody is slashed, jailed, or tombstoned, deliberately. A malicious consumer
binary can orchestrate a fork in which every honest validator signs each
conflicting header once, so the byzantine set of a verified attack is exactly
as likely to be the victim set; punishing it, at any threshold, hands the
attacker a targeting tool. The full rationale, including why this must not be
"fixed" back to slashing, lives on `HandleConsumerMisbehaviour`'s contract.

What happens after containment is a governance decision: `MsgResumeConsumer`
resumes the chain (with a forced snapshot) once the fork is understood and
fixed, or the pause expires into `STOPPED`. A resume does not bury the
evidence: while the conflicting headers remain verifiable within the client's
trusting period, re-submission pauses the consumer again, which is correct
while the fork still stands.

---

## Getting the evidence

Double-vote evidence and light-client misbehaviour originate on the consumer
chain and are observed by CometBFT / relayers there. Assembling the JSON
payloads is a client-side, off-chain task; the provider independently
re-verifies whatever is submitted, so a malformed or dishonest submission can at
worst waste the submitter's gas -- it can never fabricate a punishment.
