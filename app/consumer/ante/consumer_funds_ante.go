package ante

import (
	"context"
	"strings"

	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"

	consumertypes "github.com/allinbits/vaas/x/vaas/consumer/types"
)

type (
	ConsumerFundsKeeper interface {
		GetProviderChannel(ctx context.Context) (string, bool)
		IsConsumerInDebt(ctx context.Context) bool
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

	// Hot path: not in debt → pass without walking the message list.
	if !cfd.ConsumerKeeper.IsConsumerInDebt(ctx) {
		return next(ctx, tx, simulate)
	}

	// In debt: only let /ibc.core.* messages through so CCV/IBC liveness is
	// preserved. Relayers sign these messages directly with their own keys;
	// authz wrapping is not part of any realistic relayer flow and is
	// therefore treated as a non-IBC-core tx and rejected here.
	if isIBCCoreProtocolTx(tx.GetMsgs()) {
		return next(ctx, tx, simulate)
	}

	return ctx, errorsmod.Wrap(
		consumertypes.ErrConsumerInDebt,
		"consumer chain is in debt; non-ibc.core messages are temporarily blocked",
	)
}

func isIBCCoreProtocolTx(msgs []sdk.Msg) bool {
	if len(msgs) == 0 {
		return false
	}

	for _, msg := range msgs {
		if !strings.HasPrefix(sdk.MsgTypeURL(msg), "/ibc.core.") {
			return false
		}
	}

	return true
}
