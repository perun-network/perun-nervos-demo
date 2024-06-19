package client

import (
	"encoding/hex"
	"fmt"
	"log"
	"strconv"

	"github.com/nervosnetwork/ckb-sdk-go/v2/types"
	"perun.network/go-perun/channel"
	"perun.network/go-perun/wallet"
	"perun.network/perun-ckb-backend/channel/asset"
	address2 "perun.network/perun-ckb-backend/wallet/address"
	asset2 "perun.network/perun-demo-tui/asset"
)

type PaymentChannel struct {
	state   *channel.State
	parties []wallet.Address
	assets  []channel.Asset
}

// newPaymentChannel creates a new payment channel.
func NewPaymentChannel(state *channel.State, parties []wallet.Address, assets []channel.Asset) *PaymentChannel {
	return &PaymentChannel{
		state:   state,
		parties: parties,
		assets:  assets,
	}
}

func FormatState(c *PaymentChannel, state *channel.State, network types.Network, assetRegister asset2.Register) string {
	id := state.ID
	fstPartyPaymentAddr, _ := address2.AsParticipant(c.parties[0]).ToCKBAddress(network).Encode()
	sndPartyPaymentAddr, _ := address2.AsParticipant(c.parties[1]).ToCKBAddress(network).Encode()
	balAStrings := make([]string, len(c.assets))
	balBStrings := make([]string, len(c.assets))
	for i, a := range c.assets {
		switch a := a.(type) {
		case *asset.Asset:
			if a.IsCKBytes {
				balA, _ := ShannonToCKByte(state.Allocation.Balance(0, a)).Float64()
				balAStrings[i] = strconv.FormatFloat(balA, 'f', 2, 64)
				balB, _ := ShannonToCKByte(state.Allocation.Balance(1, a)).Float64()
				balBStrings[i] = strconv.FormatFloat(balB, 'f', 2, 64)
			} else {
				balAStrings[i] = state.Allocation.Balance(0, a).String()
				balBStrings[i] = state.Allocation.Balance(1, a).String()
			}
		default:
			log.Fatalf("unsupported asset type: %T", a)
		}
	}

	ret := fmt.Sprintf(
		"Channel ID: [green]%s[white]\n[red]Balances[white]:\n",
		hex.EncodeToString(id[:]),
	)
	ret += fmt.Sprintf("%s:\n", fstPartyPaymentAddr)
	for i, a := range c.assets {
		ret += fmt.Sprintf("    [green]%s[white] %s\n", balAStrings[i], assetRegister.GetName(a))
	}
	ret += fmt.Sprintf("%s:\n", sndPartyPaymentAddr)
	for i, a := range c.assets {
		ret += fmt.Sprintf("    [green]%s[white] %s\n", balBStrings[i], assetRegister.GetName(a))
	}
	ret += fmt.Sprintf("Final: [green]%t[white]\nVersion: [green]%d[white]", state.IsFinal, state.Version)
	return ret
}

func (c PaymentChannel) State() *channel.State {
	return c.state.Clone()
}
