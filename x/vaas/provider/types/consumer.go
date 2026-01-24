package types

import (
	vaastypes "github.com/allinbits/vaas/x/vaas/types"
)

func NewConsumerStates(
	chainID,
	clientID,
	channelID string,
	initialHeight uint64,
	genesis vaastypes.ConsumerGenesisState,
	pendingValsetChanges []vaastypes.ValidatorSetChangePacketData,
	slashDowntimeAck []string,
	phase ConsumerPhase,
) ConsumerState {
	return ConsumerState{
		ChainId:              chainID,
		ClientId:             clientID,
		ChannelId:            channelID,
		InitialHeight:        initialHeight,
		PendingValsetChanges: pendingValsetChanges,
		ConsumerGenesis:      genesis,
		SlashDowntimeAck:     slashDowntimeAck,
		Phase:                phase,
	}
}
