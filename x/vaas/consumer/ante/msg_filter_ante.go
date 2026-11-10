package ante

import (
	"context"
	"fmt"
	"strings"

	channeltypesv2 "github.com/cosmos/ibc-go/v10/modules/core/04-channel/v2/types"

	errorsmod "cosmossdk.io/errors"

	sdk "github.com/cosmos/cosmos-sdk/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"

	consumertypes "github.com/allinbits/vaas/x/vaas/consumer/types"
)

type (
	// ConsumerKeeper defines the interface required by the consumer-side
	// admission gate.
	ConsumerKeeper interface {
		GetProviderClientID(ctx context.Context) (string, bool)
		IsConsumerInDebt(ctx context.Context) bool
		IsVSCStale(ctx context.Context) bool
	}

	// MsgFilterDecorator is the consumer-side tx admission gate. It runs in
	// four modes driven by consumer state:
	//
	//   bootstrap  : provider IBC client not yet established -> only /ibc.* msgs
	//               are accepted, so the chain can stand up its IBC stack.
	//   normal     : provider client established, consumer is paying its fees
	//               and has a fresh validator set -> everything passes.
	//   in debt    : provider client established, consumer has fallen behind on
	//               per-block fees -> restricted mode (see below).
	//   stale VSC  : provider client established, no VSC packet received within
	//               the safe-mode threshold -> restricted mode (see below).
	//
	// Restricted mode: only /ibc.core.* (minus the user-originated
	// MsgSendPacket), /cosmos.gov.* and a bank send of photon vouchers pass,
	// keeping VAAS liveness, governance and the relayer's refill available so
	// the chain can recover without off-chain coordination, and value in.
	MsgFilterDecorator struct {
		ConsumerKeeper ConsumerKeeper
	}
)

func NewMsgFilterDecorator(k ConsumerKeeper) MsgFilterDecorator {
	return MsgFilterDecorator{
		ConsumerKeeper: k,
	}
}

// isAllowedBootstrapTx reports whether every message in the tx is allowed
// while no provider client is pinned: IBC messages, the pin message itself,
// and governance.
func isAllowedBootstrapTx(msgs []sdk.Msg) bool {
	for _, msg := range msgs {
		msgType := sdk.MsgTypeURL(msg)
		switch {
		case strings.HasPrefix(msgType, "/ibc."):
		case strings.HasPrefix(msgType, "/cosmos.gov."):
		case msgType == sdk.MsgTypeURL(&consumertypes.MsgSetProviderClient{}):
		default:
			return false
		}
	}
	return true
}

func (mfd MsgFilterDecorator) AnteHandle(ctx sdk.Context, tx sdk.Tx, simulate bool, next sdk.AnteHandler) (newCtx sdk.Context, err error) {
	if _, ok := mfd.ConsumerKeeper.GetProviderClientID(ctx); !ok {
		// Pre-pin: the chain has no synced validator set, so only what the
		// bootstrap itself needs may pass. That is /ibc.* (the relayer setting
		// up clients and counterparties; all IBC message types share the
		// canonical /ibc. prefix), the pin message itself
		// (MsgSetProviderClient -- a filter without it would deadlock the
		// bootstrap, unpinned because unpinned), and /cosmos.gov.* so
		// governance, where the embedding app wires it, can pin when the
		// seeded owner cannot or will not.
		if !isAllowedBootstrapTx(tx.GetMsgs()) {
			return ctx, fmt.Errorf("tx contains unsupported message types at height %d", ctx.BlockHeight())
		}
		return next(ctx, tx, simulate)
	}

	// Restricted mode: in debt, or no fresh VSC (validator set may be stale).
	//
	// Only /ibc.core.* and /cosmos.gov.* messages pass:
	//
	// /ibc.core.* preserves VAAS liveness and keeps all IBC apps working.
	// The prefix covers both IBC v1 and v2 channel/connection/client
	// messages, since the consumer chain may have v1 apps (ICS-20 v1) wired
	// in addition to VAAS itself (which uses v2). The one /ibc.core.*
	// message a user originates, /ibc.core.channel.v2.MsgSendPacket (a raw
	// ICS-20 v2 transfer), is carved out like ICS-20 v1's MsgTransfer:
	// restricted mode is for recovery, and value does not leave a chain
	// whose validator set may be stale or whose fees are unpaid. Relayers
	// sign their messages directly with their own keys; authz wrapping is
	// not part of any realistic relayer flow and is therefore treated as
	// non-ibc.core and rejected here.
	//
	// A bank send made only of the photon voucher passes too: it is how a
	// relayer that ran out of vouchers (and so cannot deliver its own refill,
	// see photon_fee_ante.go) is refilled by any account holding some; the
	// vouchers stay on the chain.
	//
	// /cosmos.gov.* keeps governance alive so the community can vote on
	// recovery (e.g. emergency funding, param changes) without requiring
	// off-chain coordination. That presumes the embedding app wires a
	// governance module; the reference consumer app wires none, so there
	// the allowance is inert.
	if mfd.ConsumerKeeper.IsConsumerInDebt(ctx) || mfd.ConsumerKeeper.IsVSCStale(ctx) {
		if isAllowedRestrictedTx(tx.GetMsgs(), mfd.voucherDenom(ctx)) {
			return next(ctx, tx, simulate)
		}
		return ctx, errorsmod.Wrap(
			consumertypes.ErrConsumerInDebt,
			"consumer is in debt or has a stale validator set; only ibc.core and cosmos.gov messages are temporarily allowed",
		)
	}
	return next(ctx, tx, simulate)
}

// voucherDenom is the photon voucher denom of the pinned provider client, or
// empty before the pin.
func (mfd MsgFilterDecorator) voucherDenom(ctx sdk.Context) string {
	clientID, ok := mfd.ConsumerKeeper.GetProviderClientID(ctx)
	if !ok {
		return ""
	}
	return ExpectedPhotonDenom(clientID)
}

// isAllowedRestrictedTx reports whether every message in the tx may pass
// restricted mode: IBC core messages minus the user-originated raw send,
// governance, and a bank send made only of the photon voucher. The last is
// the refill path: after bootstrap the relayer pays its deliveries in the
// voucher, a relayer with none cannot deliver its own refill, and restricted
// mode is exactly when deliveries matter most, so any account holding
// vouchers on the chain may hand it some. The vouchers stay on the chain.
func isAllowedRestrictedTx(msgs []sdk.Msg, voucherDenom string) bool {
	sendPacketURL := sdk.MsgTypeURL(&channeltypesv2.MsgSendPacket{})
	for _, msg := range msgs {
		url := sdk.MsgTypeURL(msg)
		switch {
		case url == sendPacketURL:
			return false
		case strings.HasPrefix(url, "/ibc.core."), strings.HasPrefix(url, "/cosmos.gov."):
		default:
			send, ok := msg.(*banktypes.MsgSend)
			if !ok || voucherDenom == "" || len(send.Amount) == 0 {
				return false
			}
			for _, coin := range send.Amount {
				if coin.Denom != voucherDenom {
					return false
				}
			}
		}
	}
	return true
}
