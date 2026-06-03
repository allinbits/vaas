#!/bin/sh
# Provider chain initialization script for VAAS e2e tests.
# This script is mounted into the provider init container and executed
# as the entrypoint. It initializes the chain, adds keys, and configures
# the node for e2e testing.
set -e

BINARY="${BINARY:-provider}"
HOME_DIR="${HOME_DIR:-/home/nonroot/.provider}"
VAL1_HOME_DIR="${HOME_DIR}-val1"
CHAIN_ID="${CHAIN_ID:-provider-e2e}"
DENOM="${DENOM:-uatone}"
MNEMONIC="${MNEMONIC:-abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon art}"

# Initialize chain (val0)
$BINARY init localnet --default-denom "$DENOM" --chain-id "$CHAIN_ID" --home "$HOME_DIR"

# Configure client
$BINARY config set client chain-id "$CHAIN_ID" --home "$HOME_DIR"
$BINARY config set client keyring-backend test --home "$HOME_DIR"

# Add keys for val0
$BINARY keys add val --home "$HOME_DIR" --keyring-backend test
$BINARY keys add user --home "$HOME_DIR" --keyring-backend test
echo "$MNEMONIC" | $BINARY keys add relayer --recover --home "$HOME_DIR" --keyring-backend test

# Add genesis accounts for val0
$BINARY genesis add-genesis-account val "1000000000000${DENOM}" --home "$HOME_DIR" --keyring-backend test
$BINARY genesis add-genesis-account user "1000000000${DENOM}" --home "$HOME_DIR" --keyring-backend test
$BINARY genesis add-genesis-account relayer "100000000${DENOM}" --home "$HOME_DIR" --keyring-backend test

# Initialize val1 node (separate home with its own consensus key)
$BINARY init val1 --default-denom "$DENOM" --chain-id "$CHAIN_ID" --home "$VAL1_HOME_DIR"
$BINARY keys add val1 --home "$VAL1_HOME_DIR" --keyring-backend test

# Copy val0's genesis to val1 so both share the same genesis state
cp "$HOME_DIR/config/genesis.json" "$VAL1_HOME_DIR/config/genesis.json"

# Add val1 genesis account (operates on the shared genesis)
$BINARY genesis add-genesis-account val1 "1000000000000${DENOM}" --home "$VAL1_HOME_DIR" --keyring-backend test

# Copy updated genesis back to val0 (now includes val1's account)
cp "$VAL1_HOME_DIR/config/genesis.json" "$HOME_DIR/config/genesis.json"

# Create gentx for val0
$BINARY genesis gentx val "1000000000${DENOM}" --home "$HOME_DIR" --keyring-backend test --chain-id "$CHAIN_ID"

# Create gentx for val1 (smaller self-delegation so it has <2/3 of voting power)
$BINARY genesis gentx val1 "100000000${DENOM}" --home "$VAL1_HOME_DIR" --keyring-backend test --chain-id "$CHAIN_ID"

# Copy val1's gentx to val0's gentx directory
cp "$VAL1_HOME_DIR"/config/gentx/*.json "$HOME_DIR/config/gentx/"

# Collect all gentxs
$BINARY genesis collect-gentxs --home "$HOME_DIR"

# Copy final genesis to val1
cp "$HOME_DIR/config/genesis.json" "$VAL1_HOME_DIR/config/genesis.json"

# Configure val0 node
$BINARY config set app api.enable true --home "$HOME_DIR"
sed -i "s#^minimum-gas-prices = .*#minimum-gas-prices = \"0.01${DENOM}\"#g" "$HOME_DIR/config/app.toml"
sed -i 's#laddr = "tcp://127.0.0.1:26657"#laddr = "tcp://0.0.0.0:26657"#g' "$HOME_DIR/config/config.toml"
sed -i 's#address = "tcp://localhost:1317"#address = "tcp://0.0.0.0:1317"#g' "$HOME_DIR/config/app.toml"
sed -i 's#address = "localhost:9090"#address = "0.0.0.0:9090"#g' "$HOME_DIR/config/app.toml"

# Configure val1 node
sed -i "s#^minimum-gas-prices = .*#minimum-gas-prices = \"0.01${DENOM}\"#g" "$VAL1_HOME_DIR/config/app.toml"
sed -i 's#laddr = "tcp://127.0.0.1:26657"#laddr = "tcp://0.0.0.0:26657"#g' "$VAL1_HOME_DIR/config/config.toml"
sed -i 's#address = "tcp://localhost:1317"#address = "tcp://0.0.0.0:1317"#g' "$VAL1_HOME_DIR/config/app.toml"
sed -i 's#address = "localhost:9090"#address = "0.0.0.0:9090"#g' "$VAL1_HOME_DIR/config/app.toml"

find "$HOME_DIR" -mindepth 1 -exec chmod 777 {} +
find "$VAL1_HOME_DIR" -mindepth 1 -exec chmod 777 {} +

echo "Provider init complete (2 validators)."
