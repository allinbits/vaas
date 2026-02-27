package ante

import (
	"context"
	"strings"

	consumertypes "github.com/allinbits/vaas/x/vaas/consumer/types"

	errorsmod "cosmossdk.io/errors"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/authz"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
)

const defaultFundingDenom = "uatone"

type (
	ConsumerFundsKeeper interface {
		GetProviderChannel(ctx context.Context) (string, bool)
		GetFeeCollectorAccountAddress(ctx context.Context) (sdk.AccAddress, bool)
		HasFeeCollectorFunds(ctx context.Context, denoms []string) bool
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

	requiredDenoms := getRequiredFundingDenoms(ctx)

	if cfd.ConsumerKeeper.HasFeeCollectorFunds(ctx, requiredDenoms) {
		return next(ctx, tx, simulate)
	}

	feeCollectorAddr, found := cfd.ConsumerKeeper.GetFeeCollectorAccountAddress(ctx)
	if !found {
		return ctx, errorsmod.Wrap(consumertypes.ErrConsumerAccountUnderfunded, "consumer fee collector account not found")
	}

	if isTopUpTx(tx.GetMsgs(), feeCollectorAddr.String(), requiredDenoms) {
		return next(ctx, tx, simulate)
	}

	return ctx, errorsmod.Wrapf(
		consumertypes.ErrConsumerAccountUnderfunded,
		"consumer fee collector account %s has no funds in required denoms (%s)",
		feeCollectorAddr.String(),
		strings.Join(requiredDenoms, ","),
	)
}

func getRequiredFundingDenoms(ctx sdk.Context) []string {
	seenDenoms := map[string]struct{}{}
	requiredDenoms := make([]string, 0, len(ctx.MinGasPrices()))
	for _, decCoin := range ctx.MinGasPrices() {
		if decCoin.Denom == "" {
			continue
		}
		if _, exists := seenDenoms[decCoin.Denom]; exists {
			continue
		}
		seenDenoms[decCoin.Denom] = struct{}{}
		requiredDenoms = append(requiredDenoms, decCoin.Denom)
	}

	if len(requiredDenoms) == 0 {
		return []string{defaultFundingDenom}
	}
	return requiredDenoms
}

func isTopUpTx(msgs []sdk.Msg, feeCollectorAddr string, denoms []string) bool {
	if len(msgs) == 0 {
		return false
	}

	allowedDenoms := map[string]struct{}{}
	for _, denom := range denoms {
		if denom != "" {
			allowedDenoms[denom] = struct{}{}
		}
	}

	for _, msg := range msgs {
		if !isTopUpMsg(msg, feeCollectorAddr, allowedDenoms) {
			return false
		}
	}

	return true
}

func isTopUpMsg(msg sdk.Msg, feeCollectorAddr string, allowedDenoms map[string]struct{}) bool {
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
		nestedMsgs, err := m.GetMessages()
		if err != nil || len(nestedMsgs) == 0 {
			return false
		}

		for _, nestedMsg := range nestedMsgs {
			if !isTopUpMsg(nestedMsg, feeCollectorAddr, allowedDenoms) {
				return false
			}
		}
		return true
	}

	return false
}

func hasAllowedPositiveCoins(coins sdk.Coins, allowedDenoms map[string]struct{}) bool {
	for _, coin := range coins {
		if _, allowed := allowedDenoms[coin.Denom]; allowed && coin.Amount.IsPositive() {
			return true
		}
	}
	return false
}
