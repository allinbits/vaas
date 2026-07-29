# Equivocation and Light-Client Evidence

A consumer chain is secured by the provider's validators, but the provider does
not watch consumer consensus directly. Instead, anyone can submit evidence of
validator misconduct on a consumer to the provider, which verifies it and (for
double-signing) punishes the offender on the provider chain.

Two evidence types exist. Both punish an identifiable offender the same way --
slash, jail, and tombstone at the double-sign level -- and differ only in how
the evidence is verified and in what happens when the provider ends up unable to
punish anyone:

| Evidence | Message | Consequence |
|---|---|---|
| Double-sign (duplicate vote) | `MsgSubmitConsumerDoubleVoting` | slash + jail + tombstone |
| Light-client attack (IBC misbehaviour) | `MsgSubmitConsumerMisbehaviour` | byzantine signers slashed + jailed + tombstoned; an attack that leaves no punishable validator stops the consumer instead |

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
   see [key-assignment.md](key-assignment.md)) and applies the global
   `InfractionParameters.DoubleSign` through `punishEquivocation`: **slash** the
   stake (default 5%), **jail**, and **tombstone**. Repeated submissions of
   already-processed evidence are idempotent (already-tombstoned is not an
   error).

On success the provider emits `vaas_submit_consumer_double_voting`; see
[events-reference.md](events-reference.md).

The slash fraction, jail duration, and tombstone flag are the global infraction
parameters (see [params-reference.md](params-reference.md) section 2); with the
defaults, a double-signer is slashed 5% and permanently removed.

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

### Verification and punishment

`HandleConsumerMisbehaviour`
([consumer_equivocation.go](../x/vaas/provider/keeper/consumer_equivocation.go))
punishes an identifiable light-client attack at the same severity as
double-signing:

1. `CheckMisbehaviour` verifies the chain id and client id match the consumer,
   that the two headers are at the same height and within the client trusting
   period, and that they genuinely conflict (different block id hashes, each
   valid against its trusted consensus state).
2. `GetByzantineValidators` extracts the validators that signed both conflicting
   headers -- the byzantine set.
3. Each byzantine validator is punished through the shared equivocation path
   (`punishEquivocation`, the same primitive double-voting uses), applying the
   global `InfractionParameters.DoubleSign`: **slash**, **jail**, and
   **tombstone**. An already-tombstoned validator is a no-op, so repeated
   submissions are idempotent.

### When nobody can be punished: terminal escalation

The escalation does not trigger on amnesia specifically. It triggers whenever a
*verified* light-client attack produces **no punished validator at all**
(`len(punished) == 0` in `HandleConsumerMisbehaviour`). Two ways to get there:

- An **amnesia** attack has no byzantine set by construction
  (`GetByzantineValidators` returns empty), so there is nobody to attribute.
- A non-empty byzantine set whose members cannot be punished -- most plainly,
  validators that have all since **unbonded**. `JailAndTombstoneValidator`
  refuses an unbonded validator, the error is logged, and that validator is not
  counted as punished. An already-tombstoned validator, by contrast, *does*
  count as punished, which is what keeps repeat submissions idempotent.

In either case the provider stops the consumer and schedules it for removal
(`escalateUnpunishableLightClientAttack` calling
`StopAndPrepareForConsumerRemoval`). **This is terminal.** No code path leaves
`CONSUMER_PHASE_STOPPED`: `MsgResumeConsumer` requires `PAUSED`,
`MsgRemoveConsumer` requires `LAUNCHED` or `PAUSED`, and no cancel or veto
message exists. The consumer is deleted once the provider unbonding period
elapses, and a chain that wants to run again must register a new consumer.

The escalation is a no-op if the consumer has already left `LAUNCHED`, so
re-submitting the same evidence does not schedule the removal twice. Either way
the submission surfaces the outcome.

---

## Getting the evidence

Double-vote evidence and light-client misbehaviour originate on the consumer
chain and are observed by CometBFT / relayers there. Assembling the JSON
payloads is a client-side, off-chain task; the provider independently
re-verifies whatever is submitted, so a malformed or dishonest submission can at
worst waste the submitter's gas -- it can never fabricate a punishment.
