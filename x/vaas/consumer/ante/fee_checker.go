package ante

import (
	"context"
	"math"
	"strings"

	channeltypesv2 "github.com/cosmos/ibc-go/v10/modules/core/04-channel/v2/types"

	errorsmod "cosmossdk.io/errors"
	sdkmath "cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	authante "github.com/cosmos/cosmos-sdk/x/auth/ante"

	consumertypes "github.com/allinbits/vaas/x/vaas/consumer/types"
)

// BootstrapKeeper is the fee checker's dependency: whether the consumer still
// bootstraps, i.e. has no routable provider client pinned, or enforces
// photon-only fees with no voucher on the chain at all (see feeBootstrap).
type BootstrapKeeper interface {
	PhotonFeesEnabled(ctx context.Context) bool
	GetProviderClientID(ctx context.Context) (string, bool)
	HasRoutableProviderClient(ctx context.Context) bool
	HasSupply(ctx context.Context, denom string) bool
}

// feeBootstrap reports whether the fee gates treat the chain as
// bootstrapping, waiving the photon policy and the node fee floor for
// bootstrap infrastructure traffic (isBootstrapInfrastructureTx). That is the
// case while no routable provider client is pinned, and again on a
// photon-fees chain whose bank supply of the voucher denom is zero: with no
// voucher anywhere on the chain no transaction could pay a fee, the relayer
// delivery that would bring the first voucher included, so the bootstrap
// exemption reopens until one voucher exists. That rescues a chain pinned
// before its fee payers were funded, or one whose vouchers have all been
// bridged away. Vouchers paid as fees stay in the fee collector and keep the
// supply above zero, so the case does not cover a relayer that merely ran
// out (docs/consumer-launch-runbook.md section 6).
func feeBootstrap(ctx context.Context, k BootstrapKeeper) bool {
	if !k.HasRoutableProviderClient(ctx) {
		return true
	}
	if !k.PhotonFeesEnabled(ctx) {
		return false
	}
	clientID, ok := k.GetProviderClientID(ctx)
	return ok && !k.HasSupply(ctx, ExpectedPhotonDenom(clientID))
}

// NewBootstrapExemptTxFeeChecker returns the consumer's TxFeeChecker for
// ante.NewDeductFeeDecorator. It mirrors atomone-sdk's default checker (the
// node's minimum-gas-prices floor enforced at CheckTx, priority derived from
// the gas price) with one exception, in time: while the consumer has no
// routable provider client, a transaction made up exclusively of bootstrap
// infrastructure messages (isBootstrapInfrastructureTx) skips the floor, so a
// chain that starts with no token at all can have its clients created, its
// counterparties registered, its first fee vouchers delivered and its
// provider client pinned, at mempools that already run a floor.
//
// The exemption ends with the pin becoming routable, which a pin set through
// MsgSetProviderClient is from the start, and reopens only while a
// photon-fees chain holds no voucher at all (feeBootstrap). Otherwise every
// transaction pays the floor like it pays the photon policy, the relayer's
// deliveries included:
// a standing exemption for infrastructure messages let anyone grow the
// chain's state for free (creating clients, updating clients of their own)
// at a cost no node setting could put back. The relayer of a photon-fees
// consumer is therefore funded with vouchers before the pin and runs with a
// gas price in the voucher denom after it (docs/consumer-launch-runbook.md).
//
// Floors on a photon-only chain belong in the voucher denom, or at zero: a
// native-denom floor would reject the photon fees user transactions must pay.
func NewBootstrapExemptTxFeeChecker(k BootstrapKeeper) authante.TxFeeChecker {
	return func(ctx sdk.Context, tx sdk.Tx) (sdk.Coins, int64, error) {
		feeTx, ok := tx.(sdk.FeeTx)
		if !ok {
			return nil, 0, errorsmod.Wrap(sdkerrors.ErrTxDecode, "Tx must be a FeeTx")
		}

		feeCoins := feeTx.GetFee()
		gas := feeTx.GetGas()

		// The floor is local mempool policy, so it only runs on CheckTx; the
		// bootstrap carve-out applies here and nowhere else. The floor itself
		// matches atomone-sdk's default checker.
		if ctx.IsCheckTx() && !(isBootstrapInfrastructureTx(tx.GetMsgs()) && feeBootstrap(ctx, k)) {
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
}

// isBootstrapInfrastructureTx reports whether every message in the tx is
// what the bootstrap itself sends: IBC core messages (clients, counterparties
// and packet deliveries) minus the user-originated raw send, the pin, and
// governance. Narrower than the admission filter's pre-pin allowance, which
// also lets the transfer application's own messages through: those are user
// traffic and pay the floor from the first block.
func isBootstrapInfrastructureTx(msgs []sdk.Msg) bool {
	if len(msgs) == 0 {
		return false
	}
	sendPacketURL := sdk.MsgTypeURL(&channeltypesv2.MsgSendPacket{})
	pinURL := sdk.MsgTypeURL(&consumertypes.MsgSetProviderClient{})
	for _, msg := range msgs {
		url := sdk.MsgTypeURL(msg)
		switch {
		case url == sendPacketURL:
			return false
		case url == pinURL:
		case strings.HasPrefix(url, "/ibc.core."), strings.HasPrefix(url, "/cosmos.gov."):
		default:
			return false
		}
	}
	return true
}

// txPriority is atomone-sdk's default naive priority (getTxPriority): the
// smallest per-denom gas price across the fee coins, capped at math.MaxInt64
// for a price beyond int64. The one difference is a zero-gas transaction,
// which gets priority zero here rather than dividing by zero.
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
