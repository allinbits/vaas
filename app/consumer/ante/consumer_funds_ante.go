package ante

import (
	"context"

	errorsmod "cosmossdk.io/errors"
	sdkmath "cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/authz"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"

	consumertypes "github.com/allinbits/vaas/x/vaas/consumer/types"
)

const (
	defaultFundingDenom = "uatone"
	maxAuthzExecDepth   = 4
)

type (
	ConsumerFundsKeeper interface {
		GetProviderChannel(ctx context.Context) (string, bool)
		GetFeeCollectorAccountAddress(ctx context.Context) (sdk.AccAddress, bool)
		HasFeeCollectorFundsForCoin(ctx context.Context, requiredCoin sdk.Coin) bool
	}

	ConsumerFundsDecorator struct {
		ConsumerKeeper ConsumerFundsKeeper
	}
)

func NewConsumerFundsDecorator(k ConsumerFundsKeeper) ConsumerFundsDecorator {
	return ConsumerFundsDecorator{
		ConsumerKeeper: k,
	}
}

func (cfd ConsumerFundsDecorator) AnteHandle(ctx sdk.Context, tx sdk.Tx, simulate bool, next sdk.AnteHandler) (newCtx sdk.Context, err error) {
	// Keep startup/bootstrap flow unchanged until the CCV channel is established.
	if _, hasProviderChannel := cfd.ConsumerKeeper.GetProviderChannel(ctx); !hasProviderChannel {
		return next(ctx, tx, simulate)
	}

	requiredCoin := getRequiredFundingCoin(ctx, tx)

	if cfd.ConsumerKeeper.HasFeeCollectorFundsForCoin(ctx, requiredCoin) {
		return next(ctx, tx, simulate)
	}

	feeCollectorAddr, found := cfd.ConsumerKeeper.GetFeeCollectorAccountAddress(ctx)
	if !found {
		return ctx, errorsmod.Wrap(consumertypes.ErrConsumerAccountUnderfunded, "consumer fee collector account not found")
	}

	if isTopUpTx(tx.GetMsgs(), feeCollectorAddr.String(), requiredCoin.Denom) {
		return next(ctx, tx, simulate)
	}

	return ctx, errorsmod.Wrapf(
		consumertypes.ErrConsumerAccountUnderfunded,
		"consumer fee collector account %s has insufficient funds; required %s",
		feeCollectorAddr.String(),
		requiredCoin.String(),
	)
}

func getBillingDenom(ctx sdk.Context) string {
	for _, decCoin := range ctx.MinGasPrices() {
		if decCoin.Denom == "" {
			continue
		}
		return decCoin.Denom
	}
	return defaultFundingDenom
}

func getRequiredFundingCoin(ctx sdk.Context, tx sdk.Tx) sdk.Coin {
	billingDenom := getBillingDenom(ctx)
	feeTx, ok := tx.(sdk.FeeTx)
	if ok {
		feeAmount := feeTx.GetFee().AmountOf(billingDenom)
		if feeAmount.IsPositive() {
			return sdk.NewCoin(billingDenom, feeAmount)
		}

		minGasRequired := getRequiredMinGasFee(ctx, feeTx.GetGas(), billingDenom)
		if minGasRequired.Amount.IsPositive() {
			return minGasRequired
		}
	}

	// Fallback: require a minimal positive amount in known gas denoms so
	// the gating still protects the chain even for non-standard/zero-fee txs.
	return sdk.NewInt64Coin(billingDenom, 1)
}

func getRequiredMinGasFee(ctx sdk.Context, gas uint64, denom string) sdk.Coin {
	if gas == 0 {
		return sdk.NewCoin(denom, sdkmath.ZeroInt())
	}

	for _, gasPrice := range ctx.MinGasPrices() {
		if gasPrice.Denom != denom {
			continue
		}

		gasDec := sdkmath.LegacyNewDec(int64(gas))
		feeAmount := gasPrice.Amount.Mul(gasDec).Ceil().RoundInt()
		return sdk.NewCoin(denom, feeAmount)
	}

	return sdk.NewCoin(denom, sdkmath.ZeroInt())
}

func isTopUpTx(msgs []sdk.Msg, feeCollectorAddr string, denom string) bool {
	if len(msgs) == 0 {
		return false
	}

	for _, msg := range msgs {
		if !isTopUpMsg(msg, feeCollectorAddr, denom, 0) {
			return false
		}
	}

	return true
}

func isTopUpMsg(msg sdk.Msg, feeCollectorAddr string, denom string, authzDepth int) bool {
	switch m := msg.(type) {
	case *banktypes.MsgSend:
		if m.ToAddress != feeCollectorAddr {
			return false
		}
		return hasAllowedPositiveCoins(m.Amount, denom)

	case *banktypes.MsgMultiSend:
		// Keep the top-up surface narrow while underfunded: exactly one output
		// and it must fund the fee collector.
		if len(m.Outputs) != 1 {
			return false
		}

		output := m.Outputs[0]
		if output.Address != feeCollectorAddr {
			return false
		}

		return hasAllowedPositiveCoins(output.Coins, denom)

	case *authz.MsgExec:
		// Bound nested MsgExec recursion to avoid pathological CPU usage.
		if authzDepth >= maxAuthzExecDepth {
			return false
		}

		nestedMsgs, err := m.GetMessages()
		if err != nil || len(nestedMsgs) == 0 {
			return false
		}

		for _, nestedMsg := range nestedMsgs {
			if !isTopUpMsg(nestedMsg, feeCollectorAddr, denom, authzDepth+1) {
				return false
			}
		}
		return true
	}

	return false
}

func hasAllowedPositiveCoins(coins sdk.Coins, denom string) bool {
	for _, coin := range coins {
		if coin.Denom == denom && coin.Amount.IsPositive() {
			return true
		}
	}
	return false
}
