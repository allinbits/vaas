package ante

import (
	"math"

	errorsmod "cosmossdk.io/errors"
	sdkmath "cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
)

// InfrastructureExemptTxFeeChecker is the consumer's TxFeeChecker for
// ante.NewDeductFeeDecorator. It mirrors the SDK default (the node's
// minimum-gas-prices floor enforced at CheckTx, priority derived from the gas
// price) with one change: a transaction made up exclusively of infrastructure
// messages (see isInfrastructureTx) skips the floor.
//
// The photon policy exempts those messages in consensus so the relayer
// traffic that carries the fee vouchers in can always flow; without this
// checker, a validator raising minimum-gas-prices would price that same
// traffic at its own mempool door, recreating the bootstrap problem one node
// at a time. The trade is that infrastructure traffic is feeless at every
// mempool by code rather than by node configuration -- the accepted cost of
// relayer exemptions, bounded by the proof work those messages demand of
// their sender.
//
// Floors on a photon-only chain belong in the voucher denom, or at zero: a
// native-denom floor would reject the photon fees user transactions must pay.
func InfrastructureExemptTxFeeChecker(ctx sdk.Context, tx sdk.Tx) (sdk.Coins, int64, error) {
	feeTx, ok := tx.(sdk.FeeTx)
	if !ok {
		return nil, 0, errorsmod.Wrap(sdkerrors.ErrTxDecode, "Tx must be a FeeTx")
	}

	feeCoins := feeTx.GetFee()
	gas := feeTx.GetGas()

	// The floor is local mempool policy, so it only runs on CheckTx, and the
	// infrastructure carve-out applies here and nowhere else: everything
	// below matches the SDK's default checker.
	if ctx.IsCheckTx() && !isInfrastructureTx(tx.GetMsgs()) {
		minGasPrices := ctx.MinGasPrices()
		if !minGasPrices.IsZero() {
			requiredFees := make(sdk.Coins, len(minGasPrices))

			// fee = ceil(minGasPrice * gasLimit), per required denom.
			glDec := sdkmath.LegacyNewDec(int64(gas))
			for i, gp := range minGasPrices {
				fee := gp.Amount.Mul(glDec)
				requiredFees[i] = sdk.NewCoin(gp.Denom, fee.Ceil().RoundInt())
			}

			if !feeCoins.IsAnyGTE(requiredFees) {
				return nil, 0, errorsmod.Wrapf(sdkerrors.ErrInsufficientFee,
					"insufficient fees; got: %s required: %s", feeCoins, requiredFees)
			}
		}
	}

	return feeCoins, txPriority(feeCoins, int64(gas)), nil
}

// txPriority mirrors the SDK default's naive priority: the smallest
// per-denom gas price across the fee coins. A zero-gas transaction gets
// priority zero rather than dividing by zero.
func txPriority(fee sdk.Coins, gas int64) int64 {
	if gas == 0 {
		return 0
	}
	var priority int64
	for _, c := range fee {
		p := int64(math.MaxInt64)
		gasPrice := c.Amount.QuoRaw(gas)
		if gasPrice.IsInt64() {
			p = gasPrice.Int64()
		}
		if priority == 0 || p < priority {
			priority = p
		}
	}
	return priority
}
