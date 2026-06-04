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
