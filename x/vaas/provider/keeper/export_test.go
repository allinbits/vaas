package keeper

import (
	"time"

	"github.com/allinbits/vaas/x/vaas/provider/types"

	tmtypes "github.com/cometbft/cometbft/types"

	ibctmtypes "github.com/cosmos/ibc-go/v10/modules/light-clients/07-tendermint"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

// OverrideWindowEndTimestampForTest replaces the downtime-evidence window-end
// timestamp resolution with fn. Production code always uses the real
// IBC-client-backed implementation wired by NewKeeper (see
// Keeper.windowEndTimestamp); this exists solely so unit tests can supply an
// anchor timestamp without fabricating real IBC consensus states.
func (k *Keeper) OverrideWindowEndTimestampForTest(fn func(ctx sdk.Context, clientId string, windowEnd int64) (time.Time, error)) {
	k.windowEndTimestampFn = fn
}

// OverrideVerifyDowntimeChallengeHeaderForTest replaces the downtime
// challenge header light-client verification with fn. Production code always
// uses the real 07-tendermint light client module (see
// Keeper.verifyDowntimeChallengeHeader); this exists solely so unit tests can
// bypass fabricating a real IBC client store.
func (k *Keeper) OverrideVerifyDowntimeChallengeHeaderForTest(fn func(ctx sdk.Context, clientId string, header *ibctmtypes.Header) error) {
	k.verifyDowntimeChallengeHeaderFn = fn
}

// WindowEndTimestampForTest exposes windowEndTimestamp for tests exercising
// the real IBC-client-backed anchor resolution -- i.e. with
// windowEndTimestampFn left unset, unlike OverrideWindowEndTimestampForTest.
// Production code never calls this directly; HandleConsumerDowntime always
// goes through windowEndTimestamp itself.
func (k Keeper) WindowEndTimestampForTest(ctx sdk.Context, clientId string, windowEnd int64) (time.Time, error) {
	return k.windowEndTimestamp(ctx, clientId, windowEnd)
}

// VerifyDowntimeChallengeHeaderForTest exposes verifyDowntimeChallengeHeader
// for tests exercising the real light-client-backed verification path -- i.e.
// with verifyDowntimeChallengeHeaderFn left unset, unlike
// OverrideVerifyDowntimeChallengeHeaderForTest. Production code never calls
// this directly; HandleChallengeConsumerDowntime always goes through
// verifyDowntimeChallengeHeader itself.
func (k Keeper) VerifyDowntimeChallengeHeaderForTest(ctx sdk.Context, clientId string, header *ibctmtypes.Header) error {
	return k.verifyDowntimeChallengeHeader(ctx, clientId, header)
}

// SlashOrEscalateLightClientAttackForTest exposes slashOrEscalateLightClientAttack
// so unit tests can exercise the confirmed-light-client-attack punishment and
// chain-escalation policy directly, without fabricating a real IBC light-client
// misbehaviour proof for CheckMisbehaviour and GetByzantineValidators to verify.
func (k Keeper) SlashOrEscalateLightClientAttackForTest(ctx sdk.Context, consumerId uint64, byzantineValidators []*tmtypes.Validator) ([]types.ProviderConsAddress, error) {
	return k.slashOrEscalateLightClientAttack(ctx, consumerId, byzantineValidators)
}
