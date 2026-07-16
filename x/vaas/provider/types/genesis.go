package types

import (
	"errors"
	"fmt"

	vaastypes "github.com/allinbits/vaas/x/vaas/types"

	errorsmod "cosmossdk.io/errors"
	"cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

func NewGenesisState(
	vscID uint64,
	vscIdToHeights []ValsetUpdateIdToHeight,
	consumerStates []ConsumerState,
	params Params,
	validatorConsumerPubkeys []ValidatorConsumerPubKey,
	validatorsByConsumerAddr []ValidatorByConsumerAddr,
	consumerAddrsToPrune []ConsumerAddrsToPrune,
	consumerFeesPerBlockOverrides []ConsumerFeesPerBlockOverride,
	consumerFeePoolShares []ConsumerFeePoolShare,
) *GenesisState {
	return &GenesisState{
		ValsetUpdateId:                vscID,
		ValsetUpdateIdToHeight:        vscIdToHeights,
		ConsumerStates:                consumerStates,
		Params:                        params,
		ValidatorConsumerPubkeys:      validatorConsumerPubkeys,
		ValidatorsByConsumerAddr:      validatorsByConsumerAddr,
		ConsumerAddrsToPrune:          consumerAddrsToPrune,
		ConsumerFeesPerBlockOverrides: consumerFeesPerBlockOverrides,
		ConsumerFeePoolShares:         consumerFeePoolShares,
	}
}

func DefaultGenesisState() *GenesisState {
	return &GenesisState{
		// ensure that VSCID is strictly positive
		ValsetUpdateId: DefaultValsetUpdateID,
		Params:         DefaultParams(),
	}
}

func (gs GenesisState) Validate() error {
	if gs.ValsetUpdateId == 0 {
		return errorsmod.Wrap(vaastypes.ErrInvalidGenesis, "valset update ID cannot be equal to zero")
	}

	if len(gs.ValsetUpdateIdToHeight) > 0 {
		// check only the first tuple of the list since it is ordered by VSC ID
		if gs.ValsetUpdateIdToHeight[0].ValsetUpdateId == 0 {
			return errorsmod.Wrap(vaastypes.ErrInvalidGenesis, "valset update ID cannot be equal to zero")
		}
	}

	seenConsumerIds := map[uint64]bool{}
	for _, cs := range gs.ConsumerStates {
		if seenConsumerIds[cs.ConsumerId] {
			return errorsmod.Wrap(vaastypes.ErrInvalidGenesis,
				fmt.Sprintf("duplicate consumer id %d in genesis", cs.ConsumerId))
		}
		seenConsumerIds[cs.ConsumerId] = true
		if err := cs.Validate(); err != nil {
			return errorsmod.Wrap(vaastypes.ErrInvalidGenesis,
				fmt.Sprintf("%s: for consumer id %d (chain id %q)", err, cs.ConsumerId, cs.ChainId))
		}
	}

	if err := gs.Params.Validate(); err != nil {
		return err
	}

	if err := KeyAssignmentValidateBasic(gs.ValidatorConsumerPubkeys,
		gs.ValidatorsByConsumerAddr,
		gs.ConsumerAddrsToPrune,
	); err != nil {
		return err
	}

	// Build a set of known consumer ids from consumer_states.
	known := make(map[uint64]struct{}, len(gs.ConsumerStates))
	for _, cs := range gs.ConsumerStates {
		known[cs.ConsumerId] = struct{}{}
	}
	// Overrides must reference a known consumer and stay strictly above the
	// module-wide fees_per_block floor (the same invariant the msg handler and
	// UpdateParams reconciliation enforce at runtime).
	floor := gs.Params.FeesPerBlockAmount
	for _, ov := range gs.ConsumerFeesPerBlockOverrides {
		amt, ok := math.NewIntFromString(ov.Amount)
		if !ok {
			return fmt.Errorf("consumer_fees_per_block_overrides[consumer_id=%d]: amount %q is not a valid integer", ov.ConsumerId, ov.Amount)
		}
		if _, ok := known[ov.ConsumerId]; !ok {
			return fmt.Errorf("consumer_fees_per_block_overrides: orphan override for unknown consumer %d", ov.ConsumerId)
		}
		if !amt.GT(floor) {
			return fmt.Errorf("consumer_fees_per_block_overrides[consumer_id=%d]: amount %q must be greater than the global fees_per_block (%s)", ov.ConsumerId, ov.Amount, floor)
		}
	}

	if err := validateConsumerFeePoolShares(gs.ConsumerFeePoolShares, seenConsumerIds); err != nil {
		return errorsmod.Wrap(vaastypes.ErrInvalidGenesis, err.Error())
	}

	if err := validatePendingDowntimeSlashes(gs.PendingDowntimeSlashes, known); err != nil {
		return errorsmod.Wrap(vaastypes.ErrInvalidGenesis, err.Error())
	}

	for _, r := range gs.EpochShareRecords {
		if r.Share.IsNil() || r.Share.IsNegative() {
			return errorsmod.Wrap(vaastypes.ErrInvalidGenesis,
				"epoch share record: share cannot be nil or negative")
		}
	}

	if err := validateWithheldFeeRecords(gs.WithheldFeeRecords, known); err != nil {
		return errorsmod.Wrap(vaastypes.ErrInvalidGenesis, err.Error())
	}
	if err := validateLastPunishedWindowEnds(gs.LastPunishedWindowEnds, known); err != nil {
		return errorsmod.Wrap(vaastypes.ErrInvalidGenesis, err.Error())
	}
	if err := validateEpochDowntimeEntries(gs.EpochDowntimeEntries, known); err != nil {
		return errorsmod.Wrap(vaastypes.ErrInvalidGenesis, err.Error())
	}

	if gs.InfractionParameters != nil {
		if err := gs.InfractionParameters.Validate(); err != nil {
			return errorsmod.Wrap(vaastypes.ErrInvalidGenesis, fmt.Sprintf("infraction_parameters: %s", err))
		}
		if err := ValidateInfractionParamsAgainst(*gs.InfractionParameters, gs.Params.TrustingPeriodFraction); err != nil {
			return errorsmod.Wrap(vaastypes.ErrInvalidGenesis, err.Error())
		}
	}

	return nil
}

// validateConsumerFeePoolShares rejects malformed share records before
// InitGenesis would otherwise panic on them: bad bech32, empty denom,
// non-positive shares, duplicate (consumer, depositor, denom) triples,
// and orphan consumer ids not present in ConsumerStates.
func validateConsumerFeePoolShares(
	shares []ConsumerFeePoolShare, knownConsumerIds map[uint64]bool,
) error {
	type triple struct {
		consumerId uint64
		depositor  string
		denom      string
	}
	seen := map[triple]bool{}
	for _, s := range shares {
		if _, err := sdk.AccAddressFromBech32(s.Depositor); err != nil {
			return fmt.Errorf("invalid depositor %q for consumer %d: %w",
				s.Depositor, s.ConsumerId, err)
		}
		if err := sdk.ValidateDenom(s.Denom); err != nil {
			return fmt.Errorf("invalid denom %q for consumer %d depositor %s: %w",
				s.Denom, s.ConsumerId, s.Depositor, err)
		}
		if s.Shares.IsNil() {
			return fmt.Errorf("nil shares for consumer %d depositor %s denom %s",
				s.ConsumerId, s.Depositor, s.Denom)
		}
		if !s.Shares.IsPositive() {
			return fmt.Errorf("non-positive shares for consumer %d depositor %s denom %s: %s",
				s.ConsumerId, s.Depositor, s.Denom, s.Shares)
		}
		if !knownConsumerIds[s.ConsumerId] {
			return fmt.Errorf("share record references unknown consumer %d", s.ConsumerId)
		}
		k := triple{s.ConsumerId, s.Depositor, s.Denom}
		if seen[k] {
			return fmt.Errorf("duplicate share record (consumer=%d, depositor=%s, denom=%s)",
				s.ConsumerId, s.Depositor, s.Denom)
		}
		seen[k] = true
	}
	return nil
}

// validatePendingDowntimeSlashes rejects malformed pending downtime slash
// entries: empty provider cons addr, nil/negative slash tokens, a
// missed-blocks bitmap whose length does not match ceil(span/8), orphan
// consumer references, and duplicate (consumer_id, provider_cons_addr)
// entries, which the keeper's collection key cannot represent (the second
// entry would silently overwrite the first at InitGenesis).
func validatePendingDowntimeSlashes(slashes []PendingDowntimeSlash, knownConsumerIds map[uint64]struct{}) error {
	type key struct {
		consumerId uint64
		addr       string
	}
	seen := map[key]bool{}
	for _, p := range slashes {
		if len(p.ProviderConsAddr) == 0 {
			return fmt.Errorf("pending downtime slash: provider cons addr cannot be empty")
		}
		if p.SlashTokens.IsNil() || p.SlashTokens.IsNegative() {
			return fmt.Errorf("pending downtime slash: slash tokens cannot be nil or negative")
		}
		if p.Span <= 0 {
			return fmt.Errorf("pending downtime slash: span must be positive (consumer=%d)", p.ConsumerId)
		}
		if wantLen := int((p.Span + 7) / 8); len(p.MissedBlocksBitmap) != wantLen {
			return fmt.Errorf(
				"pending downtime slash: missed blocks bitmap length %d does not match span %d (want %d bytes) (consumer=%d)",
				len(p.MissedBlocksBitmap), p.Span, wantLen, p.ConsumerId)
		}
		if _, ok := knownConsumerIds[p.ConsumerId]; !ok {
			return fmt.Errorf("pending downtime slash references unknown consumer %d", p.ConsumerId)
		}
		k := key{p.ConsumerId, string(p.ProviderConsAddr)}
		if seen[k] {
			return fmt.Errorf("duplicate pending downtime slash for consumer %d validator %x", p.ConsumerId, p.ProviderConsAddr)
		}
		seen[k] = true
	}
	return nil
}

// validateWithheldFeeRecords rejects duplicate (consumer_id,
// provider_cons_addr) entries and orphan consumer references; the keeper's
// collection key cannot represent duplicates, and an orphan would never be
// reachable at runtime (see the fee-pool-share orphan check above).
func validateWithheldFeeRecords(records []WithheldFeeRecord, knownConsumerIds map[uint64]struct{}) error {
	type key struct {
		consumerId uint64
		addr       string
	}
	seen := map[key]bool{}
	for _, r := range records {
		if len(r.ProviderConsAddr) == 0 {
			return fmt.Errorf("withheld fee record: provider cons addr cannot be empty")
		}
		if _, ok := knownConsumerIds[r.ConsumerId]; !ok {
			return fmt.Errorf("withheld fee record references unknown consumer %d", r.ConsumerId)
		}
		k := key{r.ConsumerId, string(r.ProviderConsAddr)}
		if seen[k] {
			return fmt.Errorf("duplicate withheld fee record for consumer %d validator %x", r.ConsumerId, r.ProviderConsAddr)
		}
		seen[k] = true
	}
	return nil
}

// validateLastPunishedWindowEnds rejects duplicate (consumer_id,
// provider_cons_addr) entries and orphan consumer references.
func validateLastPunishedWindowEnds(entries []LastPunishedWindowEnd, knownConsumerIds map[uint64]struct{}) error {
	type key struct {
		consumerId uint64
		addr       string
	}
	seen := map[key]bool{}
	for _, e := range entries {
		if len(e.ProviderConsAddr) == 0 {
			return fmt.Errorf("last punished window end: provider cons addr cannot be empty")
		}
		if _, ok := knownConsumerIds[e.ConsumerId]; !ok {
			return fmt.Errorf("last punished window end references unknown consumer %d", e.ConsumerId)
		}
		k := key{e.ConsumerId, string(e.ProviderConsAddr)}
		if seen[k] {
			return fmt.Errorf("duplicate last punished window end for consumer %d validator %x", e.ConsumerId, e.ProviderConsAddr)
		}
		seen[k] = true
	}
	return nil
}

// validateEpochDowntimeEntries rejects duplicate (consumer_id,
// provider_cons_addr) entries and orphan consumer references.
func validateEpochDowntimeEntries(entries []EpochDowntimeEntry, knownConsumerIds map[uint64]struct{}) error {
	type key struct {
		consumerId uint64
		addr       string
	}
	seen := map[key]bool{}
	for _, e := range entries {
		if len(e.ProviderConsAddr) == 0 {
			return fmt.Errorf("epoch downtime entry: provider cons addr cannot be empty")
		}
		if _, ok := knownConsumerIds[e.ConsumerId]; !ok {
			return fmt.Errorf("epoch downtime entry references unknown consumer %d", e.ConsumerId)
		}
		k := key{e.ConsumerId, string(e.ProviderConsAddr)}
		if seen[k] {
			return fmt.Errorf("duplicate epoch downtime entry for consumer %d validator %x", e.ConsumerId, e.ProviderConsAddr)
		}
		seen[k] = true
	}
	return nil
}

// Validate performs a phase-aware consumer state validation.
// Each phase has different required and forbidden fields, mirroring the
// invariants the keeper maintains (see x/vaas/provider/keeper/consumer_lifecycle.go).
func (cs ConsumerState) Validate() error {
	if cs.ChainId == "" {
		return errors.New("chain id cannot be empty")
	}
	if cs.OwnerAddress == "" {
		return errors.New("owner address cannot be empty")
	}
	if _, err := sdk.AccAddressFromBech32(cs.OwnerAddress); err != nil {
		return fmt.Errorf("invalid owner address %q: %w", cs.OwnerAddress, err)
	}
	for _, pVSC := range cs.PendingValsetChanges {
		if pVSC.ValsetUpdateId == 0 {
			return errors.New("valset update ID cannot be equal to zero")
		}
	}

	switch cs.Phase {
	case CONSUMER_PHASE_REGISTERED:
		// Pre-launch: no IBC client, no consumer genesis, no init params, no removal time.
		if cs.ClientId != "" {
			return fmt.Errorf("client id must be empty for phase %s", cs.Phase)
		}
		if cs.InitParams != nil {
			return fmt.Errorf("init params must be empty for phase %s", cs.Phase)
		}
		if cs.RemovalTime != nil {
			return fmt.Errorf("removal time must be empty for phase %s", cs.Phase)
		}

	case CONSUMER_PHASE_INITIALIZED:
		// Pre-launch but configured: init_params required; still no IBC client.
		if cs.InitParams == nil {
			return fmt.Errorf("init params required for phase %s", cs.Phase)
		}
		if cs.ClientId != "" {
			return fmt.Errorf("client id must be empty for phase %s", cs.Phase)
		}
		if cs.RemovalTime != nil {
			return fmt.Errorf("removal time must be empty for phase %s", cs.Phase)
		}

	case CONSUMER_PHASE_LAUNCHED:
		// Live: init_params + IBC client + consumer genesis all required.
		if cs.InitParams == nil {
			return fmt.Errorf("init params required for phase %s", cs.Phase)
		}
		if cs.ClientId == "" {
			return fmt.Errorf("client id required for phase %s", cs.Phase)
		}
		if err := cs.ConsumerGenesis.Validate(); err != nil {
			return err
		}
		if cs.RemovalTime != nil {
			return fmt.Errorf("removal time must be empty for phase %s", cs.Phase)
		}

	case CONSUMER_PHASE_STOPPED:
		// Scheduled for removal: LAUNCHED requirements plus a removal time.
		if cs.InitParams == nil {
			return fmt.Errorf("init params required for phase %s", cs.Phase)
		}
		if cs.ClientId == "" {
			return fmt.Errorf("client id required for phase %s", cs.Phase)
		}
		if err := cs.ConsumerGenesis.Validate(); err != nil {
			return err
		}
		if cs.RemovalTime == nil {
			return fmt.Errorf("removal time required for phase %s", cs.Phase)
		}

	case CONSUMER_PHASE_PAUSED:
		// Live but paused (see PauseConsumerChain): LAUNCHED requirements plus
		// a scheduled auto-stop time; mutually exclusive with removal_time
		// since a consumer is never both PAUSED and STOPPED at once.
		if cs.InitParams == nil {
			return fmt.Errorf("init params required for phase %s", cs.Phase)
		}
		if cs.ClientId == "" {
			return fmt.Errorf("client id required for phase %s", cs.Phase)
		}
		if err := cs.ConsumerGenesis.Validate(); err != nil {
			return err
		}
		if cs.RemovalTime != nil {
			return fmt.Errorf("removal time must be empty for phase %s", cs.Phase)
		}
		if cs.PauseExpirationTime == nil {
			return fmt.Errorf("pause expiration time required for phase %s", cs.Phase)
		}

	case CONSUMER_PHASE_DELETED:
		// Tombstoned: keeper retains owner+metadata+init_params for explorer UX
		// (see consumer_lifecycle.go DeleteConsumerChain comment).
		// Everything else is cleared.
		if cs.InitParams == nil {
			return fmt.Errorf("init params required for phase %s", cs.Phase)
		}
		if cs.Metadata == nil {
			return fmt.Errorf("metadata required for phase %s", cs.Phase)
		}
		if cs.ClientId != "" {
			return fmt.Errorf("client id must be empty for phase %s", cs.Phase)
		}
		if cs.RemovalTime != nil {
			return fmt.Errorf("removal time must be empty for phase %s", cs.Phase)
		}

	default:
		return fmt.Errorf("invalid phase: %s", cs.Phase)
	}

	return nil
}
