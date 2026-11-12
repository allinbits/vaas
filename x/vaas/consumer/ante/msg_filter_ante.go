package ante

import (
	"context"
	"fmt"
	"strings"

	channeltypesv2 "github.com/cosmos/ibc-go/v10/modules/core/04-channel/v2/types"

	errorsmod "cosmossdk.io/errors"

	sdk "github.com/cosmos/cosmos-sdk/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	govv1 "github.com/cosmos/cosmos-sdk/x/gov/types/v1"
	govv1beta1 "github.com/cosmos/cosmos-sdk/x/gov/types/v1beta1"

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
	//   bootstrap  : no provider client pinned yet -> only /ibc.* messages,
	//               /cosmos.gov.* and the pin itself (MsgSetProviderClient)
	//               are accepted, so the chain can stand up its IBC stack
	//               and declare the client.
	//   normal     : provider client established, consumer is paying its fees
	//               and has a fresh validator set -> everything passes.
	//   in debt    : provider client established, consumer has fallen behind on
	//               per-block fees -> restricted mode (see below).
	//   stale VSC  : provider client established, no VSC packet received within
	//               the safe-mode threshold -> restricted mode (see below).
	//
	// Restricted mode: only /ibc.core.* (minus the user-originated
	// MsgSendPacket), /cosmos.gov.* (a proposal only when every message it
	// carries would pass on its own) and a bank send of photon vouchers
	// pass, keeping VAAS liveness, the client recovery and the relayer's
	// refill available, and value in.
	MsgFilterDecorator struct {
		ConsumerKeeper ConsumerKeeper
	}
)

func NewMsgFilterDecorator(k ConsumerKeeper) MsgFilterDecorator {
	return MsgFilterDecorator{
		ConsumerKeeper: k,
	}
}

// ownerExecFlattened returns msgs with every MsgOwnerExec replaced by the
// messages it carries, so the admission and fee rules judge the owner's
// transactions by what they execute (a recovery of the provider client is
// IBC core traffic whoever submits it; a parameter change is not). An owner
// exec whose messages cannot be unpacked is kept as is, and no rule admits
// it; the handler refuses a nested one, which stays an owner exec here for
// the same reason.
func ownerExecFlattened(msgs []sdk.Msg) []sdk.Msg {
	out := make([]sdk.Msg, 0, len(msgs))
	for _, msg := range msgs {
		exec, ok := msg.(*consumertypes.MsgOwnerExec)
		if !ok {
			out = append(out, msg)
			continue
		}
		inner, err := exec.GetMsgs()
		if err != nil || len(inner) == 0 {
			out = append(out, msg)
			continue
		}
		out = append(out, inner...)
	}
	return out
}

// isAllowedBootstrapTx reports whether every message in the tx is allowed
// while no provider client is pinned: IBC messages, the pin message itself,
// and governance. An owner exec counts as the messages it carries
// (ownerExecFlattened).
func isAllowedBootstrapTx(msgs []sdk.Msg) bool {
	for _, msg := range ownerExecFlattened(msgs) {
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
	// Only /ibc.core.* messages (minus the raw MsgSendPacket), /cosmos.gov.*
	// messages and bank sends made only of the photon voucher pass:
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
	// On-chain recovery needs nothing else: relayer traffic, and for an
	// expired provider client a client recovery (MsgRecoverClient), both
	// IBC core, an owner exec carrying the recovery included (it is judged
	// by what it carries). A fix that needs new code is an off-chain
	// coordinated halt or hard fork, not an on-chain upgrade, so no upgrade
	// or parameter change is admitted, by the owner or anyone else.
	//
	// /cosmos.gov.* keeps a wired governance module's own traffic (votes,
	// deposits) alive. A proposal is admitted only when every message it
	// carries passes these same rules, so governance cannot do in restricted
	// mode what a direct transaction may not; a legacy content proposal
	// (gov v1beta1 MsgSubmitProposal, or MsgExecLegacyContent anywhere) is
	// refused, since its content cannot be judged. A proposal submitted
	// before restricted mode began still executes in EndBlock when its
	// voting period ends, outside the ante, which restricted mode cannot
	// bound. The reference consumer app wires no governance, so there the
	// allowance is inert.
	if mfd.ConsumerKeeper.IsConsumerInDebt(ctx) || mfd.ConsumerKeeper.IsVSCStale(ctx) {
		if isAllowedRestrictedTx(tx.GetMsgs(), mfd.voucherDenom(ctx)) {
			return next(ctx, tx, simulate)
		}
		return ctx, errorsmod.Wrap(
			consumertypes.ErrConsumerInDebt,
			"consumer is in debt or has a stale validator set; only ibc.core messages other than a raw send packet, cosmos.gov messages (a proposal only when what it carries is allowed too) and bank sends of the photon voucher are temporarily allowed",
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
// An owner exec counts as the messages it carries (ownerExecFlattened). A
// gov v1 proposal passes only when the messages it carries pass these same
// rules; a legacy content proposal, whose content cannot be judged, never
// does.
func isAllowedRestrictedTx(msgs []sdk.Msg, voucherDenom string) bool {
	sendPacketURL := sdk.MsgTypeURL(&channeltypesv2.MsgSendPacket{})
	legacyProposalURL := sdk.MsgTypeURL(&govv1beta1.MsgSubmitProposal{})
	legacyContentURL := sdk.MsgTypeURL(&govv1.MsgExecLegacyContent{})
	for _, msg := range ownerExecFlattened(msgs) {
		url := sdk.MsgTypeURL(msg)
		switch {
		case url == sendPacketURL, url == legacyProposalURL, url == legacyContentURL:
			return false
		case strings.HasPrefix(url, "/ibc.core."):
		case strings.HasPrefix(url, "/cosmos.gov."):
			proposal, ok := msg.(*govv1.MsgSubmitProposal)
			if !ok {
				continue
			}
			carried, err := proposal.GetMsgs()
			if err != nil || !isAllowedRestrictedTx(carried, voucherDenom) {
				return false
			}
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
