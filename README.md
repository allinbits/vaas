# VAAS - Validator-as-a-Service

**vaas** is a simplified implementation of the Interchain Security (ICS) protocol, derived from [interchain-security](https://github.com/cosmos/interchain-security). It provides core cross-chain validation functionality while removing complex features not needed for simpler deployments.

## Overview

VAAS allows Cosmos blockchains to lease their proof-of-stake security to consumer chains. All active validators on the provider chain automatically validate all consumer chains - there is no opt-in/opt-out mechanism.

## IBC v2 only

VAAS uses IBC v2 exclusively — no channel handshake, no port reservations.
The provider and consumer modules register on `ibcRouterV2` under the
application IDs `vaasprovider` and `vaasconsumer`. After a consumer launches,
a relayer (the localnet and e2e suites use
[`ts-relayer`](https://github.com/allinbits/ibc-v2-ts-relayer)) creates an
IBC v2 client on each chain pointing at the counterparty and registers the
path. The provider then discovers its consumer client at the next epoch
boundary; all VSC packets flow over that client.

Registering the v2 routes is necessary but nowhere near sufficient: a host chain
must also carry a set of wiring duties the modules cannot install themselves,
and most of them fail silently when omitted — one of them halts the provider
chain at the first consumer deletion. See
[docs/embedding.md](docs/embedding.md) for the checklist.
[`app/provider/app.go`](app/provider/app.go) demonstrates the full wiring;
[`app/consumer/app.go`](app/consumer/app.go) is a deliberately reduced reference
app, not a template.

## Features

### Kept from ICS

| Feature                  | Description                                                          |
| ------------------------ | -------------------------------------------------------------------- |
| Consumer Lifecycle       | Full lifecycle management, including the PAUSED phase                |
| Key Assignment           | Validators can use different consensus keys per consumer chain       |
| Infraction Parameters    | Global slash/jail parameters for double-sign and downtime            |
| VSC Packets              | Validator set updates sent at epoch boundaries                       |
| Double Voting Evidence   | Handle double voting evidence from consumers                         |
| Downtime Slashing        | Falsifiable downtime evidence; slash held behind a challenge window  |
| Light Client Misbehavior | Byzantine signers slashed, jailed, and tombstoned at the double-sign level |
| Consumer Metadata        | Name, description, metadata for chain discovery                      |

### Removed from ICS

| Feature                           | Reason                                           |
| --------------------------------- | ------------------------------------------------ |
| Partial Set Security (PSS)        | All validators validate all consumers            |
| Top N / Opt-In Chains             | No validator selection per consumer              |
| Power Shaping                     | No caps, allowlists, denylists, priority lists   |
| ICS Consumer Reward Distribution  | Replaced by a provider-side fee pool (see below) |
| Slash Packet Throttling           | No rate-limiting across consumers                |
| Per-Consumer Commission Rates     | Validators use same commission as provider       |
| IBC v1 Channel Support            | IBC v2 only                                      |
| Standalone-to-Consumer Changeover | Not currently supported (future work)            |

### Consumer fee model

Instead of ICS-style cross-chain reward distribution, each consumer prepays a
provider-side fee pool. Once per epoch the provider collects
`fees_per_block * blocks_per_epoch` from the pool and distributes it to the
bonded validators; a pool that cannot cover an epoch's fee flags the consumer
as in-debt and gates its user transactions. See
[docs/consumer-fee-pool.md](docs/consumer-fee-pool.md).

See [docs/consumer-transition.md](docs/consumer-transition.md) for the
consequences and requirements of a future standalone-to-consumer transition.

## Build & Test

```bash
make build              # go build ./...
make test               # unit tests (excludes e2e)
make lint               # golangci-lint

# E2E (Docker-based, spins up provider + consumer + ts-relayer)
make docker-build-all
make test-e2e
```

## Documentation

- [Localnet setup](app/README.md) — run a provider, a consumer, and `ts-relayer` locally
- [Embedding VAAS](docs/embedding.md) — the host-app wiring duties a chain integrating the modules must carry, and what breaks without each
- [Security model](docs/security-model.md) — what a deployment trusts, what it punishes, and the residual assumptions
- [Consumer launch runbook](docs/consumer-launch-runbook.md) — end-to-end operator flow from registration to a funded, launched consumer
- [Consumer lifecycle](docs/consumer-lifecycle.md) — phases, on-chain effects, operator/relayer responsibilities
- [Consumer downtime](docs/consumer-downtime.md) — detection, verifiable evidence, optimistic slashing, challenges, and the PAUSED phase
- [Consumer liveness](docs/consumer-liveness.md) — removal sweep, snapshot resync, and consumer safe mode
- [Consumer fee pool](docs/consumer-fee-pool.md) — funding, share accounting, withdrawal locks, and sweeping
- [Consumer transition](docs/consumer-transition.md) — future-work considerations for a standalone-to-consumer changeover
- [Key assignment](docs/key-assignment.md) — per-consumer consensus keys and the assignment rules
- [Equivocation and light-client evidence](docs/equivocation-evidence.md) — submitting double-voting and misbehaviour evidence, and their consequences
- [Validator obligations](docs/validator-obligations.md) — the operational duties bonding on the provider imposes
- [Parameters reference](docs/params-reference.md) — every provider and consumer parameter: type, bound, default, where set
- [Queries reference](docs/queries-reference.md) — every provider and consumer query, its CLI command, and what it returns
- [Events reference](docs/events-reference.md) — every event both modules emit, its attributes, and what is deliberately not an event
- [End-to-end tests](tests/e2e/README.md) — the Docker e2e suites and how to run and extend them
- [Contributor guide (AGENTS.md)](AGENTS.md) — architecture, build/test commands, code layout
- [Design rationale (DESIGN_RATIONALE.md)](DESIGN_RATIONALE.md) — why VAAS is shaped the way it is

## Learn More

- [ICS Documentation](https://cosmos.github.io/interchain-security/)
- [ICS Technical Specification](https://github.com/cosmos/ibc/blob/main/spec/app/ics-028-cross-chain-validation/README.md)
- [Cosmos SDK Documentation](https://docs.cosmos.network)
- [IBC Protocol](https://ibc.cosmos.network/)
