# VAAS API Reference

This document describes the gRPC queries and messages available in the VAAS module.

## Provider Module

### Queries

#### QueryConsumerGenesis

Returns the genesis state needed to start a consumer chain.

```
GET /vaas/provider/consumer_genesis/{consumer_id}
```

**Request:**
- `consumer_id` (string): The unique identifier of the consumer chain

**Response:**
- `genesis_state`: ConsumerGenesisState containing all necessary genesis data

---

#### QueryConsumerChains

Lists consumer chains filtered by phase.

```
GET /vaas/provider/consumer_chains/{phase}
```

**Request:**
- `phase` (ConsumerPhase): Optional filter (1=Registered, 2=Initialized, 3=Launched, 4=Stopped, 5=Deleted)
- `pagination`: Standard pagination parameters

**Response:**
- `chains`: List of Chain objects
- `pagination`: Pagination response

**IBC v2 Note:** Each chain includes a `client_id` field for IBC v2 routing identification.

---

#### QueryConsumerIdFromClientId

Returns the consumer ID associated with an IBC client.

```
GET /vaas/provider/consumer_id/{client_id}
```

**Request:**
- `client_id` (string): The IBC client ID tracking the consumer chain

**Response:**
- `consumer_id` (string): The consumer chain identifier

**IBC v2 Note:** This query is essential for IBC v2 routing, where client IDs replace channel IDs for consumer identification.

---

#### QueryConsumerChain

Returns detailed information about a specific consumer chain.

```
GET /vaas/provider/consumer_chain/{consumer_id}
```

**Request:**
- `consumer_id` (string): The consumer chain identifier

**Response:**
- `consumer_id`: Consumer identifier
- `chain_id`: Chain ID string
- `owner_address`: Owner of the consumer chain
- `phase`: Current lifecycle phase
- `metadata`: Chain metadata
- `init_params`: Initialization parameters
- `infraction_parameters`: Slashing configuration
- `client_id`: IBC client ID (IBC v2)

---

#### QueryValidatorConsumerAddr

Returns the consumer chain address for a validator.

```
GET /vaas/provider/validator_consumer_addr/{consumer_id}/{provider_address}
```

**Request:**
- `consumer_id` (string): The consumer chain identifier
- `provider_address` (string): Validator's provider chain address

**Response:**
- `consumer_address`: The assigned consumer chain address

---

#### QueryParams

Returns provider module parameters.

```
GET /vaas/provider/params
```

**Response:**
- `params`: Provider Params object

**IBC v2 Note:** The `template_client` field in params is deprecated. Per-consumer client configuration should be used via `ConsumerInitializationParameters`.

---

#### QueryBlocksUntilNextEpoch

Returns blocks remaining until the next epoch when VSC packets are sent.

```
GET /vaas/provider/blocks_until_next_epoch
```

**Response:**
- `blocks_until_next_epoch` (uint64): Number of blocks remaining

---

### Messages

#### MsgCreateConsumer

Creates a new consumer chain.

```protobuf
message MsgCreateConsumer {
  string submitter = 1;           // Address that will own the consumer
  string chain_id = 2;            // Chain ID for the new consumer
  ConsumerMetadata metadata = 3;  // Name, description, metadata URL
  ConsumerInitializationParameters initialization_parameters = 4;
  InfractionParameters infraction_parameters = 5;
}
```

**IBC v2 Note:** Client configuration is provided via `initialization_parameters`. The module will create an IBC client automatically, or an existing `connection_id` can be provided to reuse an existing client.

---

#### MsgUpdateConsumer

Updates an existing consumer chain configuration.

```protobuf
message MsgUpdateConsumer {
  string owner = 1;
  string consumer_id = 2;
  string new_owner_address = 3;
  ConsumerMetadata metadata = 4;
  ConsumerInitializationParameters initialization_parameters = 5;
  string new_chain_id = 6;
  InfractionParameters infraction_parameters = 7;
}
```

**Note:** `chain_id` can only be updated before the chain launches.

---

#### MsgRemoveConsumer

Stops and removes a consumer chain.

```protobuf
message MsgRemoveConsumer {
  string consumer_id = 1;
  string owner = 2;
}
```

---

#### MsgAssignConsumerKey

Assigns a different consensus key for a validator on a consumer chain.

```protobuf
message MsgAssignConsumerKey {
  string consumer_id = 1;
  string provider_addr = 2;
  string consumer_key = 3;  // JSON-encoded public key
  string signer = 4;
}
```

---

## Consumer Module

### Queries

#### QueryParams

Returns consumer module parameters.

```
GET /vaas/consumer/params
```

**Response:**
- `params`: ConsumerParams object

---

#### QueryProviderInfo

Returns information about the provider chain connection.

```
GET /vaas/consumer/provider-info
```

**Response:**
- `consumer`: Consumer chain info (chainID, clientID, connectionID, channelID)
- `provider`: Provider chain info (chainID, clientID, connectionID, channelID)

**IBC v2 Note:** This query supports both IBC v1 and v2. In IBC v2:
- `clientID` is the primary routing identifier
- `channelID` and `connectionID` may be empty for v2-only deployments

---

## Data Types

### ConsumerPhase

```protobuf
enum ConsumerPhase {
  CONSUMER_PHASE_UNSPECIFIED = 0;
  CONSUMER_PHASE_REGISTERED = 1;   // Consumer ID assigned
  CONSUMER_PHASE_INITIALIZED = 2;  // Parameters set, not yet launched
  CONSUMER_PHASE_LAUNCHED = 3;     // Running and consuming validation
  CONSUMER_PHASE_STOPPED = 4;      // Stopped (timeout or explicit)
  CONSUMER_PHASE_DELETED = 5;      // State deleted
}
```

### ConsumerMetadata

```protobuf
message ConsumerMetadata {
  string name = 1;         // Human-readable chain name
  string description = 2;  // Chain description
  string metadata = 3;     // Additional metadata (e.g., GitHub URL)
}
```

### InfractionParameters

```protobuf
message InfractionParameters {
  SlashJailParameters double_sign = 1;  // Double signing penalties
  SlashJailParameters downtime = 2;     // Downtime penalties
}

message SlashJailParameters {
  string slash_fraction = 1;   // Decimal fraction to slash
  Duration jail_duration = 2;  // Jail duration
  bool tombstone = 3;          // Whether to tombstone validator
}
```

## IBC v2 Considerations

### Application Identifiers

VAAS uses the following application IDs for IBC v2 routing:

| App ID | Module |
|--------|--------|
| `vaas/provider` | Provider module |
| `vaas/consumer` | Consumer module |

### Packet Flow

1. **Provider → Consumer (VSC Packets)**
   - Sent at epoch boundaries
   - Contains validator set updates
   - Consumer acknowledges and applies updates

2. **Out-of-Order Handling**
   - Consumer tracks `highest_valset_update_id`
   - Packets with lower IDs are acknowledged but ignored
   - Enables flexible packet delivery order

### Error Handling

- **Timeout**: Consumer is stopped and prepared for removal
- **Error Ack**: Consumer is stopped and prepared for removal
- **Success Ack**: Normal operation continues

### Deprecated Fields

The following fields are deprecated in IBC v2:

| Proto | Field | Replacement |
|-------|-------|-------------|
| `Params` | `template_client` | Per-consumer configuration |
| `ConsumerState` | `channel_id` | `client_id` |
| `GenesisState` (consumer) | `provider_channel_id` | `provider_client_id` |
| `GenesisState` (consumer) | `connection_id` | Direct client routing |
