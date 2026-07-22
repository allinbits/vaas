package keeper

import (
	"bytes"
	"fmt"

	"github.com/allinbits/vaas/x/vaas/provider/types"
	vaastypes "github.com/allinbits/vaas/x/vaas/types"

	"github.com/cometbft/cometbft/crypto/ed25519"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	tmtypes "github.com/cometbft/cometbft/types"

	ibctmtypes "github.com/cosmos/ibc-go/v10/modules/light-clients/07-tendermint"

	"cosmossdk.io/collections"
	errorsmod "cosmossdk.io/errors"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

// HandleChallengeConsumerDowntime verifies a MsgChallengeConsumerDowntime and,
// on success, cancels every pending downtime slash for the consumer (not
// just the one disproven -- see the success path below). Downtime (absence
// of signatures) can never be proven, but a specific claimed-missed height
// can be disproven by
// exhibiting the validator's signature sealed into the chain at that height
// -- see docs/consumer-downtime.md, "The challenge". Verification runs, in
// order, and returns a typed error with no state change on the first
// failure:
//
//  1. among the (possibly several) pending downtime slashes for the accused
//     validator on this consumer -- one per disjoint window -- the one whose
//     window contains ClaimedHeight exists, and its missed-blocks bitmap
//     marks ClaimedHeight as missed;
//  2. the supplied header is for ClaimedHeight+1 on the consumer's registered
//     chain id;
//  3. the header verifies against the consumer's IBC client through the
//     07-tendermint light client module, exactly as CheckMisbehaviour
//     verifies misbehaviour headers;
//  4. the supplied commit is canonically sealed into that header (its hash
//     equals the header's LastCommitHash) and is for ClaimedHeight;
//  5. the supplied pubkey self-authenticates as the accused validator, and
//     that validator's CommitSig in the commit -- Commit or Nil, never
//     Absent -- carries a valid signature over the canonical vote bytes.
//
// On success, in order: PayWithheldFees (before the phase flip -- the
// consumer's withdraw-lock only opens once it leaves LAUNCHED, so retro-pay
// must land before PauseConsumerChain moves it to PAUSED), then
// PauseConsumerChain, which itself calls CancelConsumerDowntimeState to clear
// every pending downtime slash and epoch downtime mark for the consumer (one
// proven-false bit indicts the reporting source as a whole). Calling
// CancelConsumerDowntimeState again here would be redundant:
// PauseConsumerChain already does it, and neither function touches
// WithheldFeeRecords (only DeleteConsumerChain does), so the pay-then-pause
// order is safe regardless.
func (k Keeper) HandleChallengeConsumerDowntime(ctx sdk.Context, msg *types.MsgChallengeConsumerDowntime) error {
	// Defensive: ValidateBasic already rejects a nil header or a nil/absent
	// inner SignedHeader.Header, but this is a permissionless entry point, so
	// guard the dereference below (msg.Header.Header.ChainID) again here in
	// case ValidateBasic is ever bypassed or this handler is invoked directly.
	if msg.Header == nil || msg.Header.SignedHeader == nil || msg.Header.SignedHeader.Header == nil {
		return errorsmod.Wrap(types.ErrDowntimeChallengeFailed, "header is malformed: signed_header or its inner header is nil")
	}

	consumerAddr := types.NewConsumerConsAddress(sdk.ConsAddress(msg.ValidatorAddr))
	providerAddr := k.GetProviderAddrFromConsumerAddr(ctx, msg.ConsumerId, consumerAddr)

	// 1. among the (possibly several) pending slashes for this validator on
	// this consumer, find the one whose window contains claimed_height, and
	// check that its bitmap marks it missed.
	pending, found, err := k.findPendingDowntimeSlashContaining(ctx, msg.ConsumerId, providerAddr.ToSdkConsAddr().Bytes(), msg.ClaimedHeight)
	if err != nil {
		return errorsmod.Wrapf(types.ErrDowntimeChallengeFailed,
			"looking up pending downtime slash for validator %s on consumer chain %d: %s",
			providerAddr.String(), msg.ConsumerId, err)
	}
	if !found {
		return errorsmod.Wrapf(types.ErrDowntimeChallengeFailed,
			"no pending downtime slash for validator %s on consumer chain %d claims height %d missed",
			providerAddr.String(), msg.ConsumerId, msg.ClaimedHeight)
	}

	index := msg.ClaimedHeight - pending.WindowStartHeight
	if !vaastypes.BitmapIsSet(pending.MissedBlocksBitmap, index) {
		return errorsmod.Wrapf(types.ErrDowntimeChallengeFailed,
			"claimed height %d is not marked missed in the pending downtime slash for validator %s on consumer chain %d",
			msg.ClaimedHeight, providerAddr.String(), msg.ConsumerId)
	}

	// 2. the header is for claimed_height+1 on the consumer's own chain id.
	consumerChainId, err := k.GetConsumerChainId(ctx, msg.ConsumerId)
	if err != nil {
		return errorsmod.Wrapf(types.ErrDowntimeChallengeFailed,
			"no registered chain id for consumer %d: %s", msg.ConsumerId, err)
	}
	if msg.Header.Header.ChainID != consumerChainId {
		return errorsmod.Wrapf(types.ErrDowntimeChallengeFailed,
			"header chain id (%s) does not match consumer chain id (%s) (consumerId: %d)",
			msg.Header.Header.ChainID, consumerChainId, msg.ConsumerId)
	}
	if msg.Header.Header.Height != msg.ClaimedHeight+1 {
		return errorsmod.Wrapf(types.ErrDowntimeChallengeFailed,
			"header height (%d) is not claimed_height+1 (%d)", msg.Header.Header.Height, msg.ClaimedHeight+1)
	}

	// 3. the header verifies against the consumer's IBC client.
	clientId, found := k.GetConsumerClientId(ctx, msg.ConsumerId)
	if !found {
		return errorsmod.Wrapf(types.ErrDowntimeChallengeFailed,
			"no IBC client found for consumer chain %d", msg.ConsumerId)
	}
	if err := k.verifyDowntimeChallengeHeader(ctx, clientId, msg.Header); err != nil {
		return errorsmod.Wrapf(types.ErrDowntimeChallengeFailed,
			"header for consumer chain %d failed light client verification: %s", msg.ConsumerId, err)
	}

	// 4.-5. the commit is canonically sealed into the header and is for
	// claimed_height, and it carries the accused validator's genuine
	// Commit-or-Nil signature (see verifySealedCommitSignature).
	if err := verifySealedCommitSignature(msg.LastCommit, msg.Header, consumerChainId, msg.ValidatorAddr, msg.ValidatorPubkey); err != nil {
		return err
	}

	// The challenge is proven: retro-pay withheld fees before the phase
	// flip, then pause the consumer (which cancels every pending downtime
	// slash and epoch downtime mark for it -- see the doc comment above).
	if err := k.PayWithheldFees(ctx, msg.ConsumerId); err != nil {
		return fmt.Errorf("paying withheld fees for consumer %d: %w", msg.ConsumerId, err)
	}
	if err := k.PauseConsumerChain(ctx, msg.ConsumerId); err != nil {
		return fmt.Errorf("pausing consumer %d after successful downtime challenge: %w", msg.ConsumerId, err)
	}

	ctx.EventManager().EmitEvent(sdk.NewEvent(
		vaastypes.EventTypeDowntimeChallengeSucceeded,
		sdk.NewAttribute(sdk.AttributeKeyModule, types.ModuleName),
		sdk.NewAttribute(types.AttributeConsumerId, fmt.Sprintf("%d", msg.ConsumerId)),
		sdk.NewAttribute(vaastypes.AttributeChallenger, msg.Signer),
		sdk.NewAttribute(vaastypes.AttributeProviderValidatorAddress, providerAddr.String()),
		sdk.NewAttribute(vaastypes.AttributeClaimedHeight, fmt.Sprintf("%d", msg.ClaimedHeight)),
	))

	return nil
}

// findPendingDowntimeSlashContaining searches the (possibly several) pending
// downtime slashes for (consumerId, providerConsAddr) -- one per accepted
// disjoint window -- for the one whose [WindowStartHeight,
// WindowStartHeight+Span) range contains claimedHeight. Windows for a pair
// are enforced disjoint at acceptance (see the AcceptedDowntimeWindows
// intersection check in HandleConsumerDowntime), so at
// most one match is expected; the sub-range is bounded by however many
// windows are currently pending for this single validator on this single
// consumer.
func (k Keeper) findPendingDowntimeSlashContaining(ctx sdk.Context, consumerId uint64, providerConsAddr []byte, claimedHeight int64) (types.PendingDowntimeSlash, bool, error) {
	iter, err := k.PendingDowntimeSlashes.Iterate(
		ctx, collections.NewSuperPrefixedTripleRange[uint64, []byte, int64](consumerId, providerConsAddr),
	)
	if err != nil {
		return types.PendingDowntimeSlash{}, false, err
	}
	defer iter.Close()

	for ; iter.Valid(); iter.Next() {
		val, err := iter.Value()
		if err != nil {
			return types.PendingDowntimeSlash{}, false, err
		}
		if claimedHeight >= val.WindowStartHeight && claimedHeight < val.WindowStartHeight+val.Span {
			return val, true, nil
		}
	}
	return types.PendingDowntimeSlash{}, false, nil
}

// verifySealedCommitSignature verifies that lastCommit is canonically sealed
// into header and carries a genuine signature by the accused validator:
//
// The commit's hash must equal the header's LastCommitHash, and its height
// must be the height immediately below the header's -- the claimed height,
// which HandleChallengeConsumerDowntime has already checked the header sits
// one above. A SignedHeader's own Commit is malleable (a retro-signed vote
// can be spliced into any valid +2/3 subset), but LastCommitHash is sealed
// by the chain at claimed_height+1 and cannot include a vote that was not
// part of the canonical commit.
//
// pubKeyBytes must self-authenticate as the accused validator (derive
// valAddr), and that validator's CommitSig in the commit -- Commit or Nil,
// never Absent -- must carry a valid signature over the canonical vote bytes
// for chainID. Because it is self-authenticating, this does not rely on
// current key-assignment state, so key rotation after the window cannot
// break verification.
func verifySealedCommitSignature(lastCommit *cmtproto.Commit, header *ibctmtypes.Header, chainID string, valAddr, pubKeyBytes []byte) error {
	claimedHeight := header.Header.Height - 1

	commit, err := tmtypes.CommitFromProto(lastCommit)
	if err != nil {
		return errorsmod.Wrapf(types.ErrDowntimeChallengeFailed, "invalid last_commit: %s", err)
	}
	if commit.Height != claimedHeight {
		return errorsmod.Wrapf(types.ErrDowntimeChallengeFailed,
			"last_commit height (%d) does not match claimed_height (%d)", commit.Height, claimedHeight)
	}
	if !bytes.Equal(commit.Hash(), header.Header.LastCommitHash) {
		return errorsmod.Wrap(types.ErrDowntimeChallengeFailed,
			"last_commit hash does not match header LastCommitHash")
	}

	pubKey := ed25519.PubKey(pubKeyBytes)
	if !bytes.Equal(pubKey.Address(), valAddr) {
		return errorsmod.Wrap(types.ErrDowntimeChallengeFailed,
			"validator_pubkey does not derive validator_addr")
	}

	sigIdx := -1
	for i, sig := range commit.Signatures {
		if bytes.Equal(sig.ValidatorAddress, valAddr) {
			sigIdx = i
			break
		}
	}
	if sigIdx == -1 {
		return errorsmod.Wrap(types.ErrDowntimeChallengeFailed,
			"last_commit carries no signature for validator_addr")
	}
	sig := commit.Signatures[sigIdx]
	if sig.BlockIDFlag != tmtypes.BlockIDFlagCommit && sig.BlockIDFlag != tmtypes.BlockIDFlagNil {
		return errorsmod.Wrapf(types.ErrDowntimeChallengeFailed,
			"validator_addr's commit signature has flag %d, want Commit(%d) or Nil(%d)",
			sig.BlockIDFlag, tmtypes.BlockIDFlagCommit, tmtypes.BlockIDFlagNil)
	}
	signBytes := commit.VoteSignBytes(chainID, int32(sigIdx))
	if !pubKey.VerifySignature(signBytes, sig.Signature) {
		return errorsmod.Wrap(types.ErrDowntimeChallengeFailed,
			"validator_addr's commit signature does not verify against validator_pubkey")
	}

	return nil
}

// verifyDowntimeChallengeHeader verifies header against the consumer IBC
// client identified by clientId, using the same 07-tendermint light client
// module path CheckMisbehaviour uses for misbehaviour headers: a single
// ibctmtypes.Header is itself a valid exported.ClientMessage, and
// VerifyClientMessage dispatches it to the client state's header-update
// verification (trusted-state lookup, trust-level check, trusting period).
// Tests may override this via OverrideVerifyDowntimeChallengeHeaderForTest
// when fabricating a real client store is impractical; production always
// takes this path.
func (k Keeper) verifyDowntimeChallengeHeader(ctx sdk.Context, clientId string, header *ibctmtypes.Header) error {
	if k.verifyDowntimeChallengeHeaderFn != nil {
		return k.verifyDowntimeChallengeHeaderFn(ctx, clientId, header)
	}

	lightClientModule := ibctmtypes.NewLightClientModule(k.cdc, k.clientKeeper.GetStoreProvider())
	return lightClientModule.VerifyClientMessage(ctx, clientId, header)
}
