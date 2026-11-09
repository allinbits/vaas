# VAAS end-to-end tests

Docker-based integration tests that spin up a real provider chain, a real
consumer chain, and the `ibc-v2-ts-relayer`, then exercise the full VAAS
lifecycle: consumer registration, genesis bootstrapping, IBC v2 client
discovery, VSC synchronization, downtime slashing and challenges, fee pooling
and distribution, key assignment, refusal and punishment deferral, liveness,
genesis round-trip, and, on four validator nodes per chain, the validator set
mirror, a delegation change, an offline validator's slash and recovery, a
key assignment under a node restart, and the refusal threshold; and, against
a consumer that lies, the provider's answer to a forged downtime accusation,
a fabricated double-sign and a fork.

This directory is its own Go module (`tests/e2e/go.mod`) and is excluded from the
top-level `make test` and from linting. It runs via the `make test-e2e` target
described below. Docker is required.

## Running

```
make docker-build-all   # build the chain image (cosmos/vaas-e2e); ts-relayer image is pulled
make test-e2e           # every suite, one at a time, each through test-e2e-suite
make test-e2e-suite SUITE=TestLivenessIntegrationTestSuite   # one suite, as each CI job runs it
```

`make test-e2e-suite` ([Makefile](../../Makefile)) builds the image and runs:

```
cd tests/e2e && go test -timeout=60m -v -run '^$(SUITE)$' ./... --count=1
```

It first refuses a `SUITE` that names no top-level test in this directory,
since `go test -run` passes when it matches nothing. `make test-e2e` lists the
`Test*Suite` functions and runs each through it, so every suite gets its own
timeout; a failed suite does not stop the others.

The chain image is built from
[docker/e2e.Dockerfile](docker/e2e.Dockerfile) and contains both the provider
and consumer binaries. The relayer image is
`ghcr.io/allinbits/ibc-v2-ts-relayer:latest`, pulled at runtime
([e2e_tsrelayer_test.go](e2e_tsrelayer_test.go) lines 17-18).

CI runs each suite as its own job, in parallel, through `make test-e2e-suite`
with the suite's top-level test name; the same target runs one suite locally:

```
make test-e2e-suite SUITE=TestIntegrationTestSuite
make test-e2e-suite SUITE=TestLivenessIntegrationTestSuite
```

## The suites

All are `testify` suites that embed a shared `baseTestSuite`
([base_suite_test.go](base_suite_test.go)) providing container lifecycle, chain
init, ts-relayer wiring, and exec/query helpers. Each suite launches its own
isolated set of containers (distinct chain IDs, Docker network, and host ports)
so the two can run on the same host without colliding.

### `IntegrationTestSuite` -- the main suite

Entry point `TestIntegrationTestSuite`
([e2e_setup_test.go](e2e_setup_test.go) lines 42-44). `SetupSuite` (lines 59+):
creates the Docker pool and network, initializes and starts the provider,
registers a consumer, fetches the consumer genesis from the provider,
initializes and starts the consumer, then starts the ts-relayer and creates the
IBC v2 path. Provider genesis is patched for a fast voting period, a fast epoch
(`blocks_per_epoch = 5`), a small fee amount, and shrunk downtime windows so the
challenge-gated flow completes inside a test run.

The scenarios run as an **ordered** sequence in `TestVAAS`
([e2e_test.go](e2e_test.go)); each scenario's doc comment says what it proves,
and the call-site comments in that list state the ordering constraints. The
order matters: later tests depend on consumer `"0"` staying `LAUNCHED` until
liveness removal, and the genesis round-trip runs last, since it stops the
provider container and restarts it from exported genesis.

This suite uses a realistic (~21-day) provider unbonding, so the liveness sweep
timing is not exercised here -- that is the liveness suite's job.

### `LivenessIntegrationTestSuite` -- observable liveness timing

Entry point `TestLivenessIntegrationTestSuite`
([e2e_liveness_suite_test.go](e2e_liveness_suite_test.go) lines 91-93). This
suite sets `LivenessGraceFraction = 0.75` and a short (200s) unbonding in
provider genesis so the grace (~150s), safe mode, forced VSC timeout + snapshot
resync, and auto-sweep removal are all observable within a CI run, while keeping
the relayer-derived client trusting period viable. It uses fast blocks and a
first-sync gate to keep the timing-sensitive assertions reliable. Its consumer
is registered via `testdata/create_consumer_short_unbonding.json`. Scenarios run
in order in `TestLivenessVAAS`, each with its doc comment;
`equivocation_execution_delay` is 420s and the gov voting period 180s here,
and the header comment of that file documents the timing rationale in detail.

### `MultiValidatorIntegrationTestSuite` -- four validator nodes per chain

Entry point `TestMultiValidatorIntegrationTestSuite`
([e2e_multival_suite_test.go](e2e_multival_suite_test.go)). The base suite
starts N validator nodes per chain from `baseSuiteConfig.validators` (one
consumer node per provider validator, zero or one meaning the single node the
other suites run); this suite runs four of equal power, on fast blocks and the
main suite's downtime parameters, with photon fees off. Only val0 of each chain
binds host ports and holds the keys the suite transacts from; the other nodes
are reached through `docker exec`. Four is the smallest set where one node can
stop and both chains keep producing blocks. Scenarios run in order in
`TestMultiValidatorVAAS`, each leaving the set whole for the next.

### `ByzantineConsumerIntegrationTestSuite` -- a consumer that lies

Entry point `TestByzantineConsumerIntegrationTestSuite`
([e2e_forge_suite_test.go](e2e_forge_suite_test.go)), on four validators like
the suite above. It runs a consumer that lies three ways and observes the
provider's answer to each, in the one order the protocol allows (evidence and
misbehaviour are accepted only from a launched consumer):

1. every consumer node runs `consumer-forge` and reports a live validator
   absent; the provider queues the accusation, a challenge built from the real
   chain cancels it and pauses the consumer, governance resumes it;
2. the test signs a double-vote with a validator's key and submits it; the
   provider jails at once and defers the punishment;
3. the test signs a fork of the consumer's history with every key; the
   provider freezes its client and pauses the consumer, punishing nobody, and
   a resume is refused while the client is frozen;
4. governance removes the consumer, which cancels the fabricated punishment.

`consumer-forge` ([forge/](forge/)) is the reference consumer app, unchanged,
behind one lie: within a height range given by its environment
(`FORGE_ABSENT_ADDRESS`, `FORGE_FROM_HEIGHT`, `FORGE_UNTIL_HEIGHT`) it shows
the app a validator as absent from blocks it signed. It is its own Go module,
built only into the `cosmos/vaas-e2e-forge` image (`make docker-build-forge`),
and only this suite runs it; nothing outside `tests/e2e` knows it exists. The
suite runs in the `E2E Forge` workflow, on demand and on pull requests that
touch the code whose answer it checks (`make test-e2e-forge` locally); the
other e2e jobs never build the forge.

## File layout

| File | Purpose |
|---|---|
| `doc.go` | package doc |
| `base_suite_test.go` | shared `baseTestSuite`: container lifecycle, chain init, config |
| `e2e_setup_test.go` | `IntegrationTestSuite` + its `SetupSuite` / config |
| `e2e_test.go` | `TestVAAS` ordered scenario list |
| `e2e_liveness_suite_test.go` | `LivenessIntegrationTestSuite` + `TestLivenessVAAS` |
| `e2e_multival_suite_test.go` | `MultiValidatorIntegrationTestSuite` + `TestMultiValidatorVAAS` |
| `e2e_multival_valset_test.go` | validator identities and the set mirror |
| `e2e_multival_delegation_test.go` | consumer powers and a delegation reaching the consumer's set |
| `e2e_multival_key_assignment_test.go` | a key assignment with the node restarted under the new key |
| `e2e_multival_offline_test.go` | an offline validator's downtime slash and recovery; pending-slash filters |
| `e2e_multival_refusal_test.go` | the stake-weighted refusal pause threshold, and the resume after withdrawal |
| `four_validator_helpers_test.go` | the multi-validator suites' shared genesis, bring-up, validator identities, accusation filter |
| `e2e_forge_suite_test.go` | `ByzantineConsumerIntegrationTestSuite` + `TestByzantineConsumerVAAS`; the switch to the forge |
| `e2e_forge_downtime_test.go` | a forged downtime accusation challenged away |
| `e2e_forge_equivocation_test.go` | a fabricated double-sign: immediate jail, deferred punishment |
| `e2e_forge_fork_test.go` | a fabricated fork: frozen client, pause, no punishment, refused resume |
| `e2e_forge_removal_test.go` | the removal that cancels the fabricated punishment |
| `forge/` | `consumer-forge`, its module, its unit tests and its image |
| `e2e_tsrelayer_test.go` | ts-relayer container start/stop, `add-path`, relay control |
| `e2e_vaas_test.go` | core VSC / valset-sync scenarios |
| `e2e_debt_test.go` | fee-pool debt-gating scenario |
| `e2e_downtime_slash_test.go` | downtime evidence + slash scenario |
| `e2e_downtime_challenge_test.go` | challenging a queued downtime slash with real consumer chain data |
| `e2e_fee_pool_test.go` | fee-pool send restriction, locks, gov clawback |
| `e2e_fee_distribution_test.go` | per-epoch validator payout out of a consumer's fee pool |
| `e2e_key_assignment_test.go` | consumer key assignment and the consumer switching onto it |
| `e2e_consumer_liveness_test.go` | liveness / safe-mode scenarios |
| `e2e_refusal_and_deferral_test.go` | refusal pause and resume, downtime and equivocation deferral behind removal votes, cancellation on removal; fabricated double-vote evidence helpers |
| `e2e_genesis_roundtrip_test.go` | provider export/restart round-trip |
| `gov_proposal_helpers_test.go` | submit/vote governance proposals from a test |
| `validator_identity_helpers_test.go` | one validator's address forms across both chains |
| `genesis_test.go` | genesis-file patching (consumer genesis merge, generic mutation) |
| `query_test.go`, `http_util_test.go`, `chain_test.go`, `e2e_exec_test.go`, `io.go` | query, HTTP, chain, container-exec helpers |
| `go.mod`, `go.sum` | this directory's own Go module |
| `docker/e2e.Dockerfile` | chain image (provider + consumer binaries) |
| `scripts/provider-init.sh`, `scripts/consumer-init.sh` | in-container chain init |
| `testdata/create_consumer*.json` | consumer-registration payloads (with a `CONSUMER_CHAIN_ID` placeholder) |

## Adding a scenario

1. Write a `testXxx` method on the relevant suite (`*IntegrationTestSuite`), in
   a new or existing `e2e_*.go` file. Use the base-suite helpers for container
   exec, chain queries, gov proposals, and ts-relayer control (start, pause,
   resume, `add-path`) rather than driving Docker directly.
2. Call it from the ordered sequence in `TestVAAS` (or `TestLivenessVAAS`,
   or `TestMultiValidatorVAAS` for a scenario that needs several nodes) at
   the right point. Ordering matters: the suite shares one set of containers and
   one consumer across all scenarios, so place your test where the chain is in
   the state it needs, and before anything that tears that state down.
3. If the scenario needs specific chain configuration, extend the suite's
   `patchProviderGenesis` (or add a `testdata` payload) rather than mutating
   state mid-run.

A new suite (a top-level `Test*Suite` function) also goes into the e2e matrix
in [.github/workflows/ci.yml](../../.github/workflows/ci.yml), or CI never runs
it: `make check-e2e-matrix`, a step of CI's Build job, fails while a suite is
missing from the matrix.

The relayer is available at
<https://github.com/allinbits/ibc-v2-ts-relayer>.
