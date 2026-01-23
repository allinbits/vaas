package types

import (
	"fmt"
	time "time"

	clienttypes "github.com/cosmos/ibc-go/v10/modules/core/02-client/types"

	govv1beta1 "github.com/cosmos/cosmos-sdk/x/gov/types/v1beta1"
)

const (
	ProposalTypeConsumerAddition = "ConsumerAddition"
	ProposalTypeConsumerRemoval  = "ConsumerRemoval"
)

var (
	_ govv1beta1.Content = &ConsumerAdditionProposal{}
	_ govv1beta1.Content = &ConsumerRemovalProposal{}
)

func init() {
	govv1beta1.RegisterProposalType(ProposalTypeConsumerAddition)
	govv1beta1.RegisterProposalType(ProposalTypeConsumerRemoval)
}

// NewConsumerAdditionProposal creates a new consumer addition proposal.
func NewConsumerAdditionProposal(title, description, chainID string,
	initialHeight clienttypes.Height, genesisHash, binaryHash []byte,
	spawnTime time.Time,
	historicalEntries int64,
	ccvTimeoutPeriod time.Duration,
	unbondingPeriod time.Duration,
) govv1beta1.Content {
	return &ConsumerAdditionProposal{
		Title:             title,
		Description:       description,
		ChainId:           chainID,
		InitialHeight:     initialHeight,
		GenesisHash:       genesisHash,
		BinaryHash:        binaryHash,
		SpawnTime:         spawnTime,
		HistoricalEntries: historicalEntries,
		CcvTimeoutPeriod:  ccvTimeoutPeriod,
		UnbondingPeriod:   unbondingPeriod,
	}
}

// GetTitle returns the title of a consumer addition proposal.
func (cccp *ConsumerAdditionProposal) GetTitle() string { return cccp.Title }

// GetDescription returns the description of a consumer addition proposal.
func (cccp *ConsumerAdditionProposal) GetDescription() string { return cccp.Description }

// ProposalRoute returns the routing key of a consumer addition proposal.
func (cccp *ConsumerAdditionProposal) ProposalRoute() string { return RouterKey }

// ProposalType returns the type of a consumer addition proposal.
func (cccp *ConsumerAdditionProposal) ProposalType() string {
	return ProposalTypeConsumerAddition
}

// ValidateBasic runs basic stateless validity checks
func (cccp *ConsumerAdditionProposal) ValidateBasic() error {
	return fmt.Errorf("ConsumerAdditionProposal is deprecated")
}

// String returns the string representation of the ConsumerAdditionProposal.
func (cccp *ConsumerAdditionProposal) String() string {
	return fmt.Sprintf(`CreateConsumerChain Proposal
	Title: %s
	Description: %s
	ChainID: %s
	InitialHeight: %s
	GenesisHash: %s
	BinaryHash: %s
	SpawnTime: %s
	HistoricalEntries: %d
	CcvTimeoutPeriod: %d
	UnbondingPeriod: %d`,
		cccp.Title,
		cccp.Description,
		cccp.ChainId,
		cccp.InitialHeight,
		cccp.GenesisHash,
		cccp.BinaryHash,
		cccp.SpawnTime,
		cccp.HistoricalEntries,
		cccp.CcvTimeoutPeriod,
		cccp.UnbondingPeriod)
}

// NewConsumerRemovalProposal creates a new consumer removal proposal.
func NewConsumerRemovalProposal(title, description, chainID string, stopTime time.Time) govv1beta1.Content {
	return &ConsumerRemovalProposal{
		Title:       title,
		Description: description,
		ChainId:     chainID,
		StopTime:    stopTime,
	}
}

// ProposalRoute returns the routing key of a consumer removal proposal.
func (sccp *ConsumerRemovalProposal) ProposalRoute() string { return RouterKey }

// ProposalType returns the type of a consumer removal proposal.
func (sccp *ConsumerRemovalProposal) ProposalType() string { return ProposalTypeConsumerRemoval }

// ValidateBasic runs basic stateless validity checks
func (sccp *ConsumerRemovalProposal) ValidateBasic() error {
	return fmt.Errorf("ConsumerRemovalProposal is deprecated")
}

