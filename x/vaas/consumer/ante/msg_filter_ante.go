package ante

import (
	"context"
	"fmt"
	"strings"

	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"

	consumertypes "github.com/allinbits/vaas/x/vaas/consumer/types"
)

type (
	// ConsumerKeeper defines the interface required by the consumer-side
	// admission gate.
	ConsumerKeeper interface {
		GetProviderClientID(ctx context.Context) (string, bool)
		IsConsumerInDebt(ctx context.Context) bool
	}

	// MsgFilterDecorator is the consumer-side tx admission gate. It runs in
	// three modes driven by consumer state:
	//
	//   pre-CCV : provider IBC client not yet established → only /ibc.* msgs
	//             are accepted, so the chain can stand up its IBC stack.
	//   normal  : provider client established, consumer is paying its fees →
	//             everything passes.
	//   in debt : provider client established, consumer has fallen behind on
	//             per-block fees → only /ibc.core.* and /cosmos.gov.* msgs
	//             pass, keeping CCV liveness and governance available so the
	//             chain can recover without off-chain coordination.
	MsgFilterDecorator struct {
		ConsumerKeeper ConsumerKeeper
	}
)

func NewMsgFilterDecorator(k ConsumerKeeper) MsgFilterDecorator {
	return MsgFilterDecorator{
		ConsumerKeeper: k,
	}
}

func (mfd MsgFilterDecorator) AnteHandle(ctx sdk.Context, tx sdk.Tx, simulate bool, next sdk.AnteHandler) (newCtx sdk.Context, err error) {
	if _, ok := mfd.ConsumerKeeper.GetProviderClientID(ctx); !ok {
		// pre-CCV: only /ibc.* messages until the provider client is up.
		// Note, rather than listing out all possible IBC message types, we assume
		// all IBC message types have a correct and canonical prefix -- /ibc.*
		if !hasOnlyPrefix(tx.GetMsgs(), "/ibc.") {
			return ctx, fmt.Errorf("tx contains unsupported message types at height %d", ctx.BlockHeight())
		}
		return next(ctx, tx, simulate)
	}

	// Hot path: post-CCV and not in debt → pass without walking the message list.
	if !mfd.ConsumerKeeper.IsConsumerInDebt(ctx) {
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

func hasOnlyPrefix(msgs []sdk.Msg, prefix string) bool {
	for _, msg := range msgs {
		if !strings.HasPrefix(sdk.MsgTypeURL(msg), prefix) {
			return false
		}
	}
	return true
}

func isAllowedDuringDebtTx(msgs []sdk.Msg) bool {
	for _, msg := range msgs {
		url := sdk.MsgTypeURL(msg)
		if !strings.HasPrefix(url, "/ibc.core.") && !strings.HasPrefix(url, "/cosmos.gov.") {
			return false
		}
	}
	return true
}
