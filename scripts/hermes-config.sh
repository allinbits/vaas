#!/bin/bash
# Create Hermes configuration for local VAAS testing

HERMES_CONFIG="${HOME}/.hermes/config.toml"
mkdir -p "${HOME}/.hermes"

cat > "${HERMES_CONFIG}" << 'EOF'
[global]
log_level = 'info'

[mode]
[mode.clients]
enabled = true
refresh = true
misbehaviour = true

[mode.connections]
enabled = true

[mode.channels]
enabled = true

[mode.packets]
enabled = true
clear_interval = 100
clear_on_start = true
tx_confirmation = false

[rest]
enabled = false
host = '127.0.0.1'
port = 3000

[telemetry]
enabled = false
host = '127.0.0.1'
port = 3001

[[chains]]
id = 'provider-localnet'
type = 'CosmosSdk'
rpc_addr = 'http://127.0.0.1:26657'
grpc_addr = 'http://127.0.0.1:9090'
rpc_timeout = '10s'
trusted_node = true
account_prefix = 'cosmos'
key_name = 'relayer'
key_store_type = 'Test'
store_prefix = 'ibc'
default_gas = 100000
max_gas = 3000000
gas_multiplier = 1.2
max_msg_num = 30
max_tx_size = 180000
clock_drift = '5s'
max_block_time = '30s'
ccv_consumer_chain = false

[chains.event_source]
mode = 'push'
url = 'ws://127.0.0.1:26657/websocket'
batch_delay = '500ms'

[chains.trust_threshold]
numerator = '1'
denominator = '3'

[chains.gas_price]
price = 0.01
denom = 'uatone'

[chains.packet_filter]
policy = 'allow'
list = [['consumer', '*'], ['provider', '*'], ['transfer', '*']]

[chains.address_type]
derivation = 'cosmos'

[[chains]]
id = 'consumer-localnet'
type = 'CosmosSdk'
rpc_addr = 'http://127.0.0.1:26667'
grpc_addr = 'http://127.0.0.1:9092'
rpc_timeout = '10s'
trusted_node = true
account_prefix = 'cosmos'
key_name = 'relayer'
key_store_type = 'Test'
store_prefix = 'ibc'
default_gas = 100000
max_gas = 3000000
gas_multiplier = 1.2
max_msg_num = 30
max_tx_size = 180000
clock_drift = '5s'
max_block_time = '30s'
ccv_consumer_chain = false

[chains.event_source]
mode = 'push'
url = 'ws://127.0.0.1:26667/websocket'
batch_delay = '500ms'

[chains.trust_threshold]
numerator = '1'
denominator = '3'

[chains.gas_price]
price = 0.01
denom = 'uatone'

[chains.packet_filter]
policy = 'allow'
list = [['consumer', '*'], ['provider', '*'], ['transfer', '*']]

[chains.address_type]
derivation = 'cosmos'
EOF

echo "Hermes configuration created at ${HERMES_CONFIG}"
