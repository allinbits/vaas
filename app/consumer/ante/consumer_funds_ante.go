package ante

import (
	"context"
	"strings"

	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/authz"

	consumertypes "github.com/allinbits/vaas/x/vaas/consumer/types"
)

const maxAuthzExecDepth = 4

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

	// Never block IBC core protocol txs (e.g. MsgRecvPacket) so CCV/IBC
	// liveness is preserved even while the consumer funding gate is active.
	if isIBCCoreProtocolTx(tx.GetMsgs()) {
		return next(ctx, tx, simulate)
	}

	if !cfd.ConsumerKeeper.IsConsumerInDebt(ctx) {
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
		if !isIBCCoreProtocolMsg(msg, 0) {
			return false
		}
	}

	return true
}

func isIBCCoreProtocolMsg(msg sdk.Msg, authzDepth int) bool {
	switch m := msg.(type) {
	case *authz.MsgExec:
		if authzDepth >= maxAuthzExecDepth {
			return false
		}

		nestedMsgs, err := m.GetMessages()
		if err != nil || len(nestedMsgs) == 0 {
			return false
		}

		for _, nestedMsg := range nestedMsgs {
			if !isIBCCoreProtocolMsg(nestedMsg, authzDepth+1) {
				return false
			}
		}

		return true

	default:
		msgType := sdk.MsgTypeURL(msg)
		return strings.HasPrefix(msgType, "/ibc.core.")
	}
}
