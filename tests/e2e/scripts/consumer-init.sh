#!/bin/sh
# Consumer chain initialization script for VAAS e2e tests.
# This script is mounted into the consumer init container and executed
# as the entrypoint. It initializes the chain, adds keys, and configures
# the node for e2e testing.
set -e

BINARY="${BINARY:-consumer}"
HOME_DIR="${HOME_DIR:-/home/nonroot/.consumer}"
CHAIN_ID="${CHAIN_ID:-consumer-e2e}"
DENOM="${DENOM:-uatone}"
MNEMONIC="${MNEMONIC:-abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon art}"

# Initialize chain
$BINARY init localnet --default-denom "$DENOM" --chain-id "$CHAIN_ID" --home "$HOME_DIR"

# Configure client
$BINARY config set client chain-id "$CHAIN_ID" --home "$HOME_DIR"
$BINARY config set client keyring-backend test --home "$HOME_DIR"

# Add keys
$BINARY keys add user --home "$HOME_DIR" --keyring-backend test
echo "$MNEMONIC" | $BINARY keys add relayer --recover --home "$HOME_DIR" --keyring-backend test

# Add genesis accounts
$BINARY genesis add-genesis-account user "1000000000${DENOM}" --home "$HOME_DIR" --keyring-backend test
$BINARY genesis add-genesis-account relayer "100000000${DENOM}" --home "$HOME_DIR" --keyring-backend test

# Enable REST API
$BINARY config set app api.enable true --home "$HOME_DIR"

# Set minimum gas prices
sed -i "s#^minimum-gas-prices = .*#minimum-gas-prices = \"0.01${DENOM}\"#g" "$HOME_DIR/config/app.toml"

# Bind RPC to all interfaces
sed -i 's#laddr = "tcp://127.0.0.1:26657"#laddr = "tcp://0.0.0.0:26657"#g' "$HOME_DIR/config/config.toml"

# Bind REST API to all interfaces
sed -i 's#address = "tcp://localhost:1317"#address = "tcp://0.0.0.0:1317"#g' "$HOME_DIR/config/app.toml"

# Bind gRPC to all interfaces
sed -i 's#address = "localhost:9090"#address = "0.0.0.0:9090"#g' "$HOME_DIR/config/app.toml"

echo "Consumer init complete."
