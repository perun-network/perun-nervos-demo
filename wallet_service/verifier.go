package wallet_service

import (
	"errors"
	"fmt"

	"perun.network/channel-service/rpc/proto"
	"perun.network/go-perun/wire/protobuf"
)

func verifyOpenChannelRequest(in *proto.OpenChannelRequest) error {
	prop := in.Proposal

	if prop == nil {
		return errors.New("Missing proposal")
	}

	if len(prop.Peers) != 2 {
		return errors.New("Only two party channels are supported")
	}

	baseProp := prop.BaseChannelProposal

	if baseProp == nil {
		return errors.New("Missing base channel proposal")
	}

	if baseProp.InitBals == nil {
		return errors.New("Missing initial balances")
	}

	if baseProp.InitBals.Balances == nil {
		return errors.New("Missing balance distribution in initial balances")
	}

	if len(baseProp.InitBals.Balances.Balances[0].Balance) != 2 {
		return errors.New("Only two party channels are supported")
	}
	if err := verifyAllocation(baseProp.InitBals); err != nil {
		return err
	}

	return nil
}

func verifyAllocation(allocation *protobuf.Allocation) error {
	numOfAssets := len(allocation.Assets)

	if len(allocation.Locked) != 0 {
		return errors.New("Locked funds are not supported")
	}

	bals := allocation.Balances
	if bals == nil {
		return errors.New("Missing balances")
	}

	if len(bals.Balances) != numOfAssets {
		return fmt.Errorf("Mismatch in number of assets and balances: Got %d assets, but %d assets defined in balances", numOfAssets, len(bals.Balances))
	}

	for i, bal := range bals.Balances {
		if len(bal.Balance) != 2 {
			return fmt.Errorf("Only two party channels are supported, but more than two balances found for asset %d", i)
		}
	}

	return nil
}
