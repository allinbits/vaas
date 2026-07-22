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
boundary; all VSC packets flow over that client. Wiring VAAS into a host app
just means adding the v2 routes — see [`app/provider/app.go`](app/provider/app.go)
and [`app/consumer/app.go`](app/consumer/app.go) for reference.

## Features

### Kept from ICS

| Feature                            | Description                                                                         |
| ---------------------------------- | ----------------------------------------------------------------------------------- |
| Consumer Lifecycle                 | Full lifecycle management (REGISTERED → INITIALIZED → LAUNCHED → STOPPED → DELETED) |
| Key Assignment                     | Validators can use different consensus keys per consumer chain                      |
| Per-Consumer Infraction Parameters | Customizable slash/jail parameters per consumer                                     |
| VSC Packets                        | Validator set updates sent at epoch boundaries                                      |
| Double Voting Evidence             | Handle double voting evidence from consumers                                        |
| Downtime Slashing                  | Consumer detects offline validators, sends slash packet to provider via IBC          |
| Light Client Misbehavior           | Detection and logging of misbehavior                                                |
| Consumer Metadata                  | Name, description, metadata for chain discovery                                     |
| Client/Connection Reuse            | Reuse existing IBC client when creating consumer                                    |

### Removed from ICS

| Feature                           | Reason                                         |
| --------------------------------- | ---------------------------------------------- |
| Partial Set Security (PSS)        | All validators validate all consumers          |
| Top N / Opt-In Chains             | No validator selection per consumer            |
| Power Shaping                     | No caps, allowlists, denylists, priority lists |
| Consumer Reward Distribution      | No cross-chain rewards                         |
| Slash Packet Throttling           | No rate-limiting across consumers                  |
| Per-Consumer Commission Rates     | Validators use same commission as provider     |
| IBC v1 Channel Support            | IBC v2 only                                    |
| Standalone-to-Consumer Changeover | Not currently supported (future work)          |

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
- [Consumer lifecycle](docs/consumer-lifecycle.md) — phases, on-chain effects, operator/relayer responsibilities
- [Consumer downtime](docs/consumer-downtime.md) — detection, verifiable evidence, optimistic slashing, challenges, and the PAUSED phase
- [Consumer liveness](docs/consumer-liveness.md) — removal sweep, snapshot resync, and consumer safe mode
- [Consumer fee pool](docs/consumer-fee-pool.md) — funding, share accounting, withdrawal locks, and sweeping
- [Contributor guide (AGENTS.md)](AGENTS.md) — architecture, build/test commands, code layout
- [Design rationale (DESIGN_RATIONALE.md)](DESIGN_RATIONALE.md) — why VAAS is shaped the way it is
- [Diff vs the ICS codebase VAAS ported from (REWRITE_SUMMARY.md)](REWRITE_SUMMARY.md) — what was removed/kept in the port
- [PLAN.old.md](PLAN.old.md) — the original pre-port rewrite plan, kept for archival reference

## Learn More

- [ICS Documentation](https://cosmos.github.io/interchain-security/)
- [ICS Technical Specification](https://github.com/cosmos/ibc/blob/main/spec/app/ics-028-cross-chain-validation/README.md)
- [Cosmos SDK Documentation](https://docs.cosmos.network)
- [IBC Protocol](https://ibc.cosmos.network/)
