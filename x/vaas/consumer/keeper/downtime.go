package keeper

import (
	vaastypes "github.com/allinbits/vaas/x/vaas/types"

	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"

	"cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

// TrackMissedBlocks records the previous block's votes into per-validator
// bitmaps and, when the previous height closed a tumbling window, queues one
// evidence packet per offender and resets all bitmaps.
//
// The vote for height h-1 is delivered in block h, so the window
// [S, S+W-1] closes while processing block S+W.
func (k Keeper) TrackMissedBlocks(ctx sdk.Context) {
	voteHeight := ctx.BlockHeight() - 1
	if voteHeight <= 0 {
		return
	}
	window, minSigned := k.GetDowntimeParams(ctx)
	if window <= 0 {
		return
	}
	windowStart := (voteHeight / window) * window
	idx := voteHeight - windowStart

	for _, vote := range ctx.VoteInfos() {
		addr := vote.Validator.Address
		if _, err := k.FirstTrackedHeights.Get(ctx, addr); err != nil {
			// not yet tracked: this is the validator's first sighting
			if err := k.FirstTrackedHeights.Set(ctx, addr, voteHeight); err != nil {
				panic(err)
			}
		}
		if vote.BlockIdFlag == cmtproto.BlockIDFlagAbsent {
			bitmap, err := k.MissedBlockBitmaps.Get(ctx, addr)
			if err != nil {
				bitmap = make([]byte, (window+7)/8)
			}
			bitmap[idx/8] |= 1 << (idx % 8)
			if err := k.MissedBlockBitmaps.Set(ctx, addr, bitmap); err != nil {
				panic(err)
			}
		}
	}

	if voteHeight == windowStart+window-1 {
		k.closeWindow(ctx, windowStart, window, minSigned)
	}
}

// closeWindow evaluates every validator tracked during the window that just
// closed, queues one evidence packet per offender, and resets all
// missed-block tracking state ahead of the next window.
func (k Keeper) closeWindow(ctx sdk.Context, windowStart, window int64, minSigned math.LegacyDec) {
	maxMissed := window - minSigned.MulInt64(window).Ceil().TruncateInt64()

	iter, err := k.MissedBlockBitmaps.Iterate(ctx, nil)
	if err != nil {
		panic(err)
	}
	type tracked struct {
		addr   []byte
		bitmap []byte
	}
	var offenders []tracked
	for ; iter.Valid(); iter.Next() {
		kv, err := iter.KeyValue()
		if err != nil {
			panic(err)
		}
		offenders = append(offenders, tracked{addr: kv.Key, bitmap: kv.Value})
	}
	iter.Close()

	for _, o := range offenders {
		firstTracked, err := k.FirstTrackedHeights.Get(ctx, o.addr)
		if err != nil {
			firstTracked = windowStart
		}
		first := windowStart
		if firstTracked > first {
			first = firstTracked
		}
		span := windowStart + window - first
		offset := first - windowStart

		missed := countMissedBits(o.bitmap, offset, window)
		if missed <= maxMissed {
			continue
		}

		has, err := k.PendingEvidencePackets.Has(ctx, o.addr)
		if err != nil {
			panic(err)
		}
		if has {
			continue
		}

		rebased := rebaseBitmap(o.bitmap, offset, span)
		packet := vaastypes.NewEvidencePacketData(sdk.ConsAddress(o.addr), first, rebased, span, window, minSigned)
		if err := k.QueueEvidencePacket(ctx, packet); err != nil {
			panic(err)
		}
	}

	if err := k.MissedBlockBitmaps.Clear(ctx, nil); err != nil {
		panic(err)
	}
	if err := k.FirstTrackedHeights.Clear(ctx, nil); err != nil {
		panic(err)
	}

	k.applyStagedDowntimeParams(ctx)
}

// countMissedBits counts the set bits in bitmap over the half-open bit range
// [from, to).
func countMissedBits(bitmap []byte, from, to int64) int64 {
	var count int64
	for i := from; i < to; i++ {
		byteIdx := i / 8
		if int(byteIdx) >= len(bitmap) {
			continue
		}
		if bitmap[byteIdx]&(1<<(i%8)) != 0 {
			count++
		}
	}
	return count
}

// rebaseBitmap builds a new bitmap of length ceil(span/8) bytes whose bit 0
// corresponds to bit `offset` of the source bitmap, so the returned bitmap's
// bit i represents the same height as source bit offset+i.
func rebaseBitmap(bitmap []byte, offset, span int64) []byte {
	out := make([]byte, (span+7)/8)
	for i := int64(0); i < span; i++ {
		srcIdx := offset + i
		byteIdx := srcIdx / 8
		if int(byteIdx) >= len(bitmap) {
			continue
		}
		if bitmap[byteIdx]&(1<<(srcIdx%8)) != 0 {
			out[i/8] |= 1 << (i % 8)
		}
	}
	return out
}

// GetDowntimeParams returns the tumbling-window size (in consumer blocks) and
// the minimum fraction of the window a validator must sign, as currently in
// effect on the consumer.
func (k Keeper) GetDowntimeParams(ctx sdk.Context) (window int64, minSigned math.LegacyDec) {
	params := k.GetConsumerParams(ctx)
	return params.SignedBlocksWindow, params.MinSignedPerWindow
}

// StageDowntimeParams stages new downtime parameters to take effect at the
// next window boundary, so a parameter change never applies to a window
// that is already in progress. A no-op if p already matches the current
// parameters. Rejects (logs and skips staging) params that fail
// validDowntimeParams, e.g. a VSC packet carrying an empty
// `"downtime_params": {}` (SignedBlocksWindow 0, nil-backed
// MinSignedPerWindow).
func (k Keeper) StageDowntimeParams(ctx sdk.Context, p vaastypes.DowntimeParams) {
	if !validDowntimeParams(p) {
		k.Logger(ctx).Error(
			"rejected invalid downtime params echoed on VSC packet",
			"signed_blocks_window", p.SignedBlocksWindow,
			"min_signed_per_window", p.MinSignedPerWindow,
		)
		return
	}

	current := k.GetConsumerParams(ctx)
	if current.SignedBlocksWindow == p.SignedBlocksWindow && current.MinSignedPerWindow.Equal(p.MinSignedPerWindow) {
		return
	}
	if err := k.StagedDowntimeParams.Set(ctx, p); err != nil {
		panic(err)
	}
}

// validDowntimeParams reports whether p carries a usable tumbling window
// size and minimum-signed fraction: a positive SignedBlocksWindow and a
// non-nil MinSignedPerWindow strictly between 0 and 1.
func validDowntimeParams(p vaastypes.DowntimeParams) bool {
	if p.SignedBlocksWindow <= 0 {
		return false
	}
	if p.MinSignedPerWindow.IsNil() || !p.MinSignedPerWindow.IsPositive() || !p.MinSignedPerWindow.LT(math.LegacyOneDec()) {
		return false
	}
	return true
}

// applyStagedDowntimeParams pops any staged downtime parameters into the
// stored consumer params. Called at window close so a parameter change only
// ever takes effect at a window boundary.
func (k Keeper) applyStagedDowntimeParams(ctx sdk.Context) {
	staged, err := k.StagedDowntimeParams.Get(ctx)
	if err != nil {
		return
	}
	params := k.GetConsumerParams(ctx)
	params.SignedBlocksWindow = staged.SignedBlocksWindow
	params.MinSignedPerWindow = staged.MinSignedPerWindow
	k.SetParams(ctx, params)

	if err := k.StagedDowntimeParams.Remove(ctx); err != nil {
		panic(err)
	}
}
