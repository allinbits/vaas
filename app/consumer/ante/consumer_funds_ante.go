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
		HasFeeCollectorFundsForAmount(ctx context.Context, requiredAmount sdk.Coins) bool
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

	requiredAmount := getRequiredFundingAmount(ctx, tx)

	if cfd.ConsumerKeeper.HasFeeCollectorFundsForAmount(ctx, requiredAmount) {
		return next(ctx, tx, simulate)
	}

	feeCollectorAddr, found := cfd.ConsumerKeeper.GetFeeCollectorAccountAddress(ctx)
	if !found {
		return ctx, errorsmod.Wrap(consumertypes.ErrConsumerAccountUnderfunded, "consumer fee collector account not found")
	}

	requiredDenoms := denomsFromCoins(requiredAmount)
	if isTopUpTx(tx.GetMsgs(), feeCollectorAddr.String(), requiredDenoms) {
		return next(ctx, tx, simulate)
	}

	return ctx, errorsmod.Wrapf(
		consumertypes.ErrConsumerAccountUnderfunded,
		"consumer fee collector account %s has insufficient funds; required %s",
		feeCollectorAddr.String(),
		requiredAmount.String(),
	)
}

func getBillingDenoms(ctx sdk.Context) []string {
	seen := map[string]struct{}{}
	denoms := make([]string, 0, len(ctx.MinGasPrices()))
	for _, decCoin := range ctx.MinGasPrices() {
		if decCoin.Denom == "" {
			continue
		}
		if _, ok := seen[decCoin.Denom]; ok {
			continue
		}
		seen[decCoin.Denom] = struct{}{}
		denoms = append(denoms, decCoin.Denom)
	}

	if len(denoms) == 0 {
		return []string{defaultFundingDenom}
	}
	return denoms
}

func getRequiredFundingAmount(ctx sdk.Context, tx sdk.Tx) sdk.Coins {
	billingDenoms := getBillingDenoms(ctx)
	feeTx, ok := tx.(sdk.FeeTx)
	if ok {
		fee := feeTx.GetFee()
		if !fee.IsZero() {
			return fee
		}

		minGasRequired := getRequiredMinGasFees(ctx, feeTx.GetGas(), billingDenoms)
		if !minGasRequired.IsZero() {
			return minGasRequired
		}
	}

	// Fallback: require a minimal positive amount in known gas denoms.
	minimal := make(sdk.Coins, 0, len(billingDenoms))
	for _, denom := range billingDenoms {
		minimal = append(minimal, sdk.NewInt64Coin(denom, 1))
	}
	return minimal
}

func getRequiredMinGasFees(ctx sdk.Context, gas uint64, denoms []string) sdk.Coins {
	if gas == 0 {
		return sdk.Coins{}
	}

	requiredByDenom := map[string]sdkmath.Int{}
	for _, gasPrice := range ctx.MinGasPrices() {
		if gasPrice.Denom == "" {
			continue
		}
		if _, ok := requiredByDenom[gasPrice.Denom]; ok {
			continue
		}

		gasDec := sdkmath.LegacyNewDec(int64(gas))
		feeAmount := gasPrice.Amount.Mul(gasDec).Ceil().RoundInt()
		if feeAmount.IsPositive() {
			requiredByDenom[gasPrice.Denom] = feeAmount
		}
	}

	required := make(sdk.Coins, 0, len(denoms))
	for _, denom := range denoms {
		if feeAmount, ok := requiredByDenom[denom]; ok {
			required = append(required, sdk.NewCoin(denom, feeAmount))
		}
	}

	return required
}

func denomsFromCoins(coins sdk.Coins) []string {
	denoms := make([]string, 0, len(coins))
	for _, coin := range coins {
		if coin.Denom == "" {
			continue
		}
		denoms = append(denoms, coin.Denom)
	}
	if len(denoms) == 0 {
		return []string{defaultFundingDenom}
	}
	return denoms
}

func isTopUpTx(msgs []sdk.Msg, feeCollectorAddr string, denoms []string) bool {
	if len(msgs) == 0 {
		return false
	}

	allowedDenoms := map[string]struct{}{}
	for _, denom := range denoms {
		if denom == "" {
			continue
		}
		allowedDenoms[denom] = struct{}{}
	}

	for _, msg := range msgs {
		if !isTopUpMsg(msg, feeCollectorAddr, allowedDenoms, 0) {
			return false
		}
	}

	return true
}

func isTopUpMsg(msg sdk.Msg, feeCollectorAddr string, allowedDenoms map[string]struct{}, authzDepth int) bool {
	switch m := msg.(type) {
	case *banktypes.MsgSend:
		if m.ToAddress != feeCollectorAddr {
			return false
		}
		return hasAllowedPositiveCoins(m.Amount, allowedDenoms)

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

		return hasAllowedPositiveCoins(output.Coins, allowedDenoms)

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
			if !isTopUpMsg(nestedMsg, feeCollectorAddr, allowedDenoms, authzDepth+1) {
				return false
			}
		}
		return true
	}

	return false
}

func hasAllowedPositiveCoins(coins sdk.Coins, allowedDenoms map[string]struct{}) bool {
	for _, coin := range coins {
		if _, ok := allowedDenoms[coin.Denom]; ok && coin.Amount.IsPositive() {
			return true
		}
	}
	return false
}
