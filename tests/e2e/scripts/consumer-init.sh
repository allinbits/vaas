#!/bin/sh
# Consumer chain initialization script for VAAS e2e tests.
# This script is mounted into the consumer init container and executed
# as the entrypoint. It initializes NUM_VALIDATORS node homes under
# HOMES_DIR (val0, val1, ...) sharing one genesis, and configures every node
# for e2e testing, peer lists included, so no node's configuration has to
# change once it is running. The validator keys are placed by the suite
# afterwards: each consumer node validates under its provider validator's
# consensus key.
set -e

BINARY="${BINARY:-consumer}"
HOMES_DIR="${HOMES_DIR:-/home/nonroot/homes}"
NUM_VALIDATORS="${NUM_VALIDATORS:-1}"
CHAIN_ID="${CHAIN_ID:-consumer-e2e}"
DENOM="${DENOM:-uatone}"
MIN_GAS_PRICE="${MIN_GAS_PRICE:-0.01${DENOM}}"
MNEMONIC="${MNEMONIC:-abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon art}"

home_of() {
	echo "$HOMES_DIR/val$1"
}

VAL0_HOME="$(home_of 0)"
LAST=$((NUM_VALIDATORS - 1))

# Initialize every node home.
i=0
while [ "$i" -le "$LAST" ]; do
	H="$(home_of "$i")"
	$BINARY init "val$i" --default-denom "$DENOM" --chain-id "$CHAIN_ID" --home "$H"
	$BINARY config set client chain-id "$CHAIN_ID" --home "$H"
	$BINARY config set client keyring-backend test --home "$H"
	i=$((i + 1))
done

# val0 holds the accounts the suite transacts from.
$BINARY keys add user --home "$VAL0_HOME" --keyring-backend test
echo "$MNEMONIC" | $BINARY keys add relayer --recover --home "$VAL0_HOME" --keyring-backend test
# The owner key: HD index 1 of the same mnemonic, so its address bytes exist
# on both chains (it registers the consumer and declares/pins the IBC clients)
# while staying off the relayer's account, whose sequence the ts-relayer is
# constantly consuming with its own transactions.
echo "$MNEMONIC" | $BINARY keys add owner --recover --index 1 --home "$VAL0_HOME" --keyring-backend test

# Genesis accounts, in val0's genesis, then the genesis handed to every home.
$BINARY genesis add-genesis-account user "1000000000${DENOM}" --home "$VAL0_HOME" --keyring-backend test
$BINARY genesis add-genesis-account relayer "100000000${DENOM}" --home "$VAL0_HOME" --keyring-backend test
$BINARY genesis add-genesis-account owner "100000000${DENOM}" --home "$VAL0_HOME" --keyring-backend test
i=1
while [ "$i" -le "$LAST" ]; do
	cp "$VAL0_HOME/config/genesis.json" "$(home_of "$i")/config/genesis.json"
	i=$((i + 1))
done

# Per-node configuration: serving addresses, fee floor, and the peer list
# naming every other node by the container name the suite gives it.
i=0
while [ "$i" -le "$LAST" ]; do
	H="$(home_of "$i")"

	# Enable REST API
	$BINARY config set app api.enable true --home "$H"

	# Set minimum gas prices (a photon-fees consumer launches with a zero price:
	# fee-denom policy is enforced by the ante in consensus, not by the mempool)
	sed -i "s#^minimum-gas-prices = .*#minimum-gas-prices = \"${MIN_GAS_PRICE}\"#g" "$H/config/app.toml"

	# Bind RPC, REST API and gRPC to all interfaces
	sed -i 's#laddr = "tcp://127.0.0.1:26657"#laddr = "tcp://0.0.0.0:26657"#g' "$H/config/config.toml"
	sed -i 's#address = "tcp://localhost:1317"#address = "tcp://0.0.0.0:1317"#g' "$H/config/app.toml"
	sed -i 's#address = "localhost:9090"#address = "0.0.0.0:9090"#g' "$H/config/app.toml"

	PEERS=""
	j=0
	while [ "$j" -le "$LAST" ]; do
		if [ "$j" -ne "$i" ]; then
			PEER="$($BINARY comet show-node-id --home "$(home_of "$j")")@${CHAIN_ID}-val${j}:26656"
			PEERS="${PEERS:+$PEERS,}$PEER"
		fi
		j=$((j + 1))
	done
	sed -i "s#^persistent_peers = .*#persistent_peers = \"${PEERS}\"#g" "$H/config/config.toml"
	sed -i 's#^addr_book_strict = .*#addr_book_strict = false#g' "$H/config/config.toml"
	sed -i 's#^allow_duplicate_ip = .*#allow_duplicate_ip = true#g' "$H/config/config.toml"

	i=$((i + 1))
done

find "$HOMES_DIR" -mindepth 1 -exec chmod 777 {} +

echo "Consumer init complete ($NUM_VALIDATORS validators)."
