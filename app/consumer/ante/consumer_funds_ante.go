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

	// In debt: only let /ibc.core.* and /cosmos.gov.* messages through.
	//
	// /ibc.core.* preserves CCV liveness and keeps all IBC apps working.
	// The prefix covers both IBC v1 and v2 channel/connection/client
	// messages, since the consumer chain may have v1 apps (ICS-20 v1) wired
	// in addition to VAAS itself (which uses v2). User-initiated ICS-20 v2
	// transfers go through /ibc.core.channel.v2.MsgSendPacket and are
	// therefore also allowed. Relayers sign these messages directly with
	// their own keys; authz wrapping is not part of any realistic relayer
	// flow and is therefore treated as non-ibc.core and rejected here.
	//
	// /cosmos.gov.* keeps governance alive during debt so the community can
	// vote on recovery (e.g. emergency funding, param changes) without
	// requiring off-chain coordination.
	if isAllowedDuringDebtTx(tx.GetMsgs()) {
		return next(ctx, tx, simulate)
	}

	return ctx, errorsmod.Wrap(
		consumertypes.ErrConsumerInDebt,
		"consumer chain is in debt; only ibc.core and cosmos.gov messages are temporarily allowed",
	)
}

func isAllowedDuringDebtTx(msgs []sdk.Msg) bool {
	if len(msgs) == 0 {
		return false
	}

	for _, msg := range msgs {
		url := sdk.MsgTypeURL(msg)
		if !strings.HasPrefix(url, "/ibc.core.") && !strings.HasPrefix(url, "/cosmos.gov.") {
			return false
		}
	}

	return true
}
