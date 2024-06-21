package client

import (
	"context"
	"fmt"
	"log"
	"math/big"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/nervosnetwork/ckb-sdk-go/v2/rpc"
	"github.com/nervosnetwork/ckb-sdk-go/v2/types"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"perun.network/channel-service/rpc/proto"
	gpchannel "perun.network/go-perun/channel"
	gpwallet "perun.network/go-perun/wallet"
	"perun.network/go-perun/wire/protobuf"
	perunproto "perun.network/go-perun/wire/protobuf"
	"perun.network/perun-ckb-backend/channel/asset"
	"perun.network/perun-ckb-backend/wallet"
	"perun.network/perun-ckb-backend/wallet/address"
	asset2 "perun.network/perun-demo-tui/asset"
	vc "perun.network/perun-demo-tui/client"
	"perun.network/perun-nervos-demo/wallet_service"
	"polycry.pt/poly-go/sync"
)

type WalletClient struct {
	observerMutex sync.Mutex
	balanceMutex  sync.Mutex
	observers     []vc.Observer
	Channel       *PaymentChannel
	Name          string
	balance       *big.Int
	sudtBalance   *big.Int
	Account       *wallet.Account
	Network       types.Network
	assetRegister asset2.Register

	ChannelService proto.ChannelServiceClient
	WalletServer   *wallet_service.MyWalletService
	walletService  proto.WalletServiceClient

	parties []gpwallet.Address
	assets  []gpchannel.Asset

	rpcClient rpc.Client
}

func NewWalletClient(
	name string,
	network types.Network,
	rpcURL string,
	parties []gpwallet.Address,
	assets []gpchannel.Asset,
	wsURL string,
	csURL string,
	account *wallet.Account,
	key *secp256k1.PrivateKey,
	assetRegister asset2.Register,
	wg *sync.WaitGroup,
) (*WalletClient, error) {

	// Create wallet service server / client
	wss, err := wallet_service.NewWalletServiceServer(name, account, key, network, wsURL, wg)
	if err != nil {
		return nil, fmt.Errorf("creating wallet service server: %w", err)
	}

	conn, err := grpc.Dial(wsURL, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("dialing wallet service server: %w", err)
	}
	wsc := proto.NewWalletServiceClient(conn)

	// Create channel service client
	conn, err = grpc.Dial(csURL, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("dialing channel service server: %w", err)
	}
	csc := proto.NewChannelServiceClient(conn)

	balanceRPC, err := rpc.Dial(rpcURL)
	if err != nil {
		return nil, err
	}

	p := &WalletClient{
		Name:           name,
		balance:        big.NewInt(0),
		sudtBalance:    big.NewInt(0),
		Account:        account,
		Network:        network,
		parties:        parties,
		assets:         assets,
		assetRegister:  assetRegister,
		rpcClient:      balanceRPC,
		walletService:  wsc,
		WalletServer:   wss,
		ChannelService: csc,
	}
	wss.SetOnUpdate(p.NotifyAllState)

	go p.PollBalances()
	return p, nil
}

// WalletAddress returns the wallet address of the client.
func (p *WalletClient) WalletAddress() gpwallet.Address {
	return p.Account.Address()
}

func (p *WalletClient) Register(observer vc.Observer) {
	p.observerMutex.Lock()
	defer p.observerMutex.Unlock()
	p.observers = append(p.observers, observer)
	if p.Channel != nil {
		observer.UpdateState(FormatState(p.Channel, p.Channel.State(), p.Network, p.assetRegister))
	}
	observer.UpdateBalance(FormatBalance(p.GetBalance(), p.GetSudtBalance()))
}

func (p *WalletClient) GetBalance() *big.Int {
	p.balanceMutex.Lock()
	defer p.balanceMutex.Unlock()
	return new(big.Int).Set(p.balance)
}

func (p *WalletClient) GetSudtBalance() *big.Int {
	p.balanceMutex.Lock()
	defer p.balanceMutex.Unlock()
	return new(big.Int).Set(p.sudtBalance)
}

func (p *WalletClient) Deregister(observer vc.Observer) {
	p.observerMutex.Lock()
	defer p.observerMutex.Unlock()
	for i, o := range p.observers {
		if o.GetID().String() == observer.GetID().String() {
			p.observers[i] = p.observers[len(p.observers)-1]
			p.observers = p.observers[:len(p.observers)-1]
		}

	}
}

func (p *WalletClient) NotifyAllState(from, to *gpchannel.State) {
	p.observerMutex.Lock()
	defer p.observerMutex.Unlock()
	p.Channel = NewPaymentChannel(to, p.parties, p.assets)
	str := FormatState(p.Channel, to, p.Network, p.assetRegister)
	log.Printf("Notifying all observers of state change for client %s", p.Name)
	for _, o := range p.observers {
		o.UpdateState(str)
	}
}

func (p *WalletClient) NotifyAllBalance(ckbBal int64) {
	// TODO: This is hacky and gruesome, but we make this work for this demo.
	str := FormatBalance(new(big.Int).SetInt64(ckbBal), p.GetSudtBalance())
	for _, o := range p.observers {
		o.UpdateBalance(str)
	}
}

func (p *WalletClient) DisplayName() string {
	return p.Name
}

func (p *WalletClient) DisplayAddress() string {
	addr, _ := address.AsParticipant(p.Account.Address()).ToCKBAddress(p.Network).Encode()
	return addr
}

// OpenChannel opens a new channel with the specified peer and funding.
func (p *WalletClient) OpenChannel(peer gpwallet.Address, amounts map[gpchannel.Asset]float64) {
	// We define the channel participants. The proposer always has index 0. Here
	// we use the on-chain addresses as off-chain addresses, but we could also
	// use different ones.
	log.Println("OpenChannel called")

	assets := make([]gpchannel.Asset, len(amounts))
	i := 0
	for a := range amounts {
		assets[i] = a
		i++
	}

	// We create an initial allocation which defines the starting balances.
	initAlloc := gpchannel.NewAllocation(2, assets...)
	log.Println(initAlloc.Assets)
	for a, amount := range amounts {
		switch a := a.(type) {
		case *asset.Asset:
			if a.IsCKBytes {
				initAlloc.SetAssetBalances(a, []gpchannel.Bal{
					CKByteToShannon(big.NewFloat(amount)), // Our initial balance.
					CKByteToShannon(big.NewFloat(amount)), // Peer's initial balance.
				})
			} else {
				intAmount := new(big.Int).SetUint64(uint64(amount))
				initAlloc.SetAssetBalances(a, []gpchannel.Bal{
					intAmount, // Our initial balance.
					intAmount, // Peer's initial balance.
				})
			}
		default:
			log.Fatalf("Asset is not of type *asset.Asset")
		}

	}
	log.Println("Created Allocation")

	// Create the channel open request

	requester, err := p.Account.Address().MarshalBinary()
	if err != nil {
		log.Fatalf("Failed to marshal requester address: %v", err)
	}

	peerBytes, err := peer.MarshalBinary()
	if err != nil {
		log.Fatalf("Failed to marshal peer address: %v", err)
	}

	protAlloc, err := perunproto.FromAllocation(*initAlloc)
	if err != nil {
		log.Fatalf("Failed to convert allocation to protobuf: %v", err)
	}

	challengeDuration := uint64(10) // On-chain challenge duration in seconds.

	log.Println("Created Proposal")
	openChannelRequest := &proto.ChannelOpenRequest{
		Requester:         requester,
		Peer:              peerBytes,
		Allocation:        protAlloc,
		ChallengeDuration: challengeDuration,
	}

	// Use channel service to send proposal
	resp, err := p.ChannelService.OpenChannel(context.Background(), openChannelRequest)
	if err != nil {
		log.Fatalf("Failed to open channel: %v", err)
	}

	// Check if the channel open request was rejected
	if rej, ok := resp.Msg.(*proto.ChannelOpenResponse_Rejected); ok {
		log.Fatalf("Channel open request was rejected, reason: %v", rej.Rejected.Reason)
	}

	log.Println("Sent Channel")

}

func (p *WalletClient) SendPaymentToPeer(amounts map[gpchannel.Asset]float64) {
	log.Println("SendPaymentToPeer called")
	if !p.HasOpenChannel() {
		return
	}
	var actor gpchannel.Index
	if p.Name == "Alice" {
		actor = 0
	} else {
		actor = 1
	}
	peer := 1 - actor
	for a, amount := range amounts {
		if amount < 0 {
			continue
		}
		if a.(*asset.Asset).IsCKBytes {
			shannonAmount := CKByteToShannon(big.NewFloat(amount))
			p.Channel.state.Allocation.TransferBalance(actor, peer, a, shannonAmount)
		} else {
			intAmount := new(big.Int).SetUint64(uint64(amount))
			p.Channel.state.Allocation.TransferBalance(actor, peer, a, intAmount)
		}
	}
	protoUpdate, err := protobuf.FromState(p.Channel.State())
	if err != nil {
		log.Fatalf("Failed to convert state to protobuf: %v", err)
	}
	updateChannelRequest := &proto.ChannelUpdateRequest{
		State: protoUpdate,
	}

	log.Println("Sending payment to peer")
	resp, err := p.ChannelService.UpdateChannel(context.Background(), updateChannelRequest)
	if err != nil {
		log.Fatalf("Failed to update channel: %v", err)
	}

	if rej, ok := resp.Msg.(*proto.ChannelUpdateResponse_Rejected); ok {
		log.Fatalf("Channel close request was rejected, reason: %v", rej.Rejected.Reason)
	}

	p.NotifyAllState(p.Channel.State(), p.Channel.State())
}

func (p *WalletClient) Settle() {
	log.Println("Settle called")
	if !p.HasOpenChannel() {
		return
	}

	closeChannelRequest := &proto.ChannelCloseRequest{
		ChannelId: p.Channel.State().ID[:],
	}

	resp, err := p.ChannelService.CloseChannel(context.Background(), closeChannelRequest)
	if err != nil {
		log.Fatalf("Failed to close channel: %v", err)
	}

	if rej, ok := resp.Msg.(*proto.ChannelCloseResponse_Rejected); ok {
		log.Fatalf("Channel close request was rejected, reason: %v", rej.Rejected.Reason)
	}

	p.Channel = nil

}

func (p *WalletClient) RestoreChannel() {
	if !p.HasOpenChannel() {
		// Reinit Perun Client on Channel Service
		log.Println("Creating perun client")
		resp, err := p.ChannelService.NewPerunClient(context.Background(), &proto.NewPerunClientRequest{})
		if err != nil {
			log.Fatalf("failed to create perun client: %s", err)
		}

		if !resp.Accepted {
			log.Fatalf("perun client creation rejected")
		}

		log.Println("Restoring channel")
		// Restore the channel
		resp2, err := p.ChannelService.RestoreChannels(context.Background(), &proto.RestoreChannelsRequest{})
		if err != nil {
			log.Fatalf("Failed to restore channel: %v", err)
		}

		if !resp2.Accepted {
			log.Fatalf("Channel restore request was rejected")
		}
		log.Println("Channel restored")

		return
	}
	log.Println("Channel is already online")
}

func (p *WalletClient) HasOpenChannel() bool {
	return p.Channel != nil
}

// GetOpenChannelAssets returns the assets of the client's currently open channel.
func (p *WalletClient) GetOpenChannelAssets() []gpchannel.Asset {
	if !p.HasOpenChannel() {
		return nil
	}
	return p.Channel.assets
}
