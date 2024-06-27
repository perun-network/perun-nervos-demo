package wallet_service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"

	"polycry.pt/poly-go/sync"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/nervosnetwork/ckb-sdk-go/v2/transaction"
	"github.com/nervosnetwork/ckb-sdk-go/v2/types"
	"google.golang.org/grpc"
	"perun.network/channel-service/rpc/proto"
	"perun.network/go-perun/channel"
	"perun.network/go-perun/client"
	"perun.network/go-perun/wire/protobuf"
	"perun.network/perun-ckb-backend/backend"
	"perun.network/perun-ckb-backend/channel/asset"
	"perun.network/perun-ckb-backend/wallet"
	"perun.network/perun-ckb-backend/wallet/address"
)

const (
	logFile = "wallet_service/wallet_service_%s.log"
)

// MyWalletService implements the wallet API.
type MyWalletService struct {
	account    *wallet.Account
	privateKey *secp256k1.PrivateKey
	network    types.Network
	stateMtx   sync.Mutex
	state      *channel.State
	logger     *log.Logger
	server     *grpc.Server

	signer backend.Signer

	onUpdate func(from, to *channel.State)
	onClose  func()

	proto.UnimplementedWalletServiceServer
}

// NewWalletService creates a new wallet service.
func NewWalletService(name string, acc *wallet.Account, privKey *secp256k1.PrivateKey, network types.Network) *MyWalletService {
	file, err := os.OpenFile(fmt.Sprintf(logFile, name), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		panic(err)

	}

	logger := log.New(file, "wallet_service: ", log.LstdFlags)

	ckbAddr := address.AsParticipant(acc.Address()).ToCKBAddress(network)
	signer := backend.NewSignerInstance(ckbAddr, *privKey, network)

	return &MyWalletService{
		account:    acc,
		privateKey: privKey,
		network:    network,
		logger:     logger,
		signer:     signer,
	}
}

// NewWalletServiceServer creates a new wallet service server with the url.
func NewWalletServiceServer(name string, acc *wallet.Account, privKey *secp256k1.PrivateKey, network types.Network, url string, wg *sync.WaitGroup) (*MyWalletService, error) {
	lis, err := net.Listen("tcp", url)
	if err != nil {
		return nil, err
	}
	ws := NewWalletService(name, acc, privKey, network)
	var opts []grpc.ServerOption
	s := grpc.NewServer(opts...)
	proto.RegisterWalletServiceServer(s, ws)

	go func() {

		ws.logger.Println("wallet service listening on", url)
		err := s.Serve(lis)
		if err != nil {
			panic(err)
		}
	}()
	ws.server = s
	wg.Add(1)
	return ws, nil
}

func (ws *MyWalletService) Shutdown(wg *sync.WaitGroup) {
	defer wg.Done() // Decrease the wait group counter
	ws.logger.Println("Shutting down wallet service...")
	ws.server.Stop()
	ws.logger.Println("Wallet service stopped gracefully")
}

// SetOnUpdate sets the function to be called when the state is updated.
func (wsc *MyWalletService) SetOnUpdate(onUpdate func(from, to *channel.State)) {
	wsc.onUpdate = onUpdate
}

// OpenChannel opens a channel.
func (wsc *MyWalletService) OpenChannel(ctx context.Context, in *proto.OpenChannelRequest) (*proto.OpenChannelResponse, error) {
	wsc.logger.Println("wallet: openChannelRequest")
	err := verifyOpenChannelRequest(in)
	if err != nil {
		return nil, fmt.Errorf("open channel: %w", err)
	}
	return openChannelAccepted()

}

func openChannelAccepted() (*proto.OpenChannelResponse, error) {
	nonceShare := client.WithRandomNonce()["nonce"]
	nonceShareBytes, ok := nonceShare.([32]byte)
	if !ok {
		return nil, errors.New("nonce share is not a byte array")
	}
	return &proto.OpenChannelResponse{
		Msg: &proto.OpenChannelResponse_NonceShare{
			NonceShare: nonceShareBytes[:],
		}}, nil
}

func (wsc *MyWalletService) SignMessage(ctx context.Context, in *proto.SignMessageRequest) (*proto.SignMessageResponse, error) {
	wsc.logger.Println("wallet: signMessageRequest")

	signedMsg, err := wsc.account.SignData(in.Data)
	if err != nil {
		wsc.logger.Println("Error signing message", err)
	}
	return &proto.SignMessageResponse{
		Msg: &proto.SignMessageResponse_Signature{
			Signature: signedMsg,
		},
	}, nil
}

func (wsc *MyWalletService) SignTransaction(ctx context.Context, in *proto.SignTransactionRequest) (*proto.SignTransactionResponse, error) {
	wsc.logger.Println("wallet: signTransactionRequest")

	var tx transaction.TransactionWithScriptGroups
	err := json.Unmarshal(in.Transaction, &tx)
	if err != nil {
		return nil, fmt.Errorf("sign transaction: %w", err)
	}
	wsc.logger.Printf("Signing transaction: %s\n", string(in.Transaction))
	signedTX, err := wsc.signer.SignTransaction(&tx)
	if err != nil {
		wsc.logger.Println("Error signing message", err)
	}

	signedTXBytes, err := json.Marshal(signedTX)
	if err != nil {
		return nil, fmt.Errorf("sign transaction: %w", err)

	}
	wsc.logger.Printf("Signed transaction: %s\n", string(signedTXBytes))
	return &proto.SignTransactionResponse{
		Msg: &proto.SignTransactionResponse_Transaction{
			Transaction: signedTXBytes,
		},
	}, nil
}

func (wsc *MyWalletService) UpdateNotification(ctx context.Context, in *proto.UpdateNotificationRequest) (*proto.UpdateNotificationResponse, error) {
	wsc.logger.Printf("wallet: updateNotificationRequest: state %v\n", in.State)

	state, err := toCKBState(in.State)
	if err != nil {
		return nil, fmt.Errorf("update notification: %w", err)
	}
	wsc.logger.Printf("wallet: updateNotificationRequest: balance %v\n", state.Allocation.Balances)
	wsc.onUpdate(nil, state)

	wsc.setState(state)

	return &proto.UpdateNotificationResponse{
		Accepted: true,
	}, nil
}

func (wsc *MyWalletService) GetAssets(ctx context.Context, in *proto.GetAssetsRequest) (*proto.GetAssetsResponse, error) {
	return nil, errors.New("not implemented")
}

func (wsc *MyWalletService) setState(state *channel.State) {
	wsc.stateMtx.Lock()
	defer wsc.stateMtx.Unlock()
	wsc.state = state.Clone()
}

func (wsc *MyWalletService) getState() *channel.State {
	wsc.stateMtx.Lock()
	defer wsc.stateMtx.Unlock()
	if wsc.state == nil {
		return nil
	}
	return wsc.state.Clone()
}

func toCKBState(protoState *protobuf.State) (*channel.State, error) {
	state := &channel.State{}
	copy(state.ID[:], protoState.Id)
	state.Version = protoState.Version
	state.IsFinal = protoState.IsFinal
	allocation, err := toCKBAllocation(protoState.Allocation)
	if err != nil {
		return nil, fmt.Errorf("unmarshalling state: %w", err)
	}
	state.Allocation = *allocation
	state.App, state.Data, err = toAppAndData(protoState.App, protoState.Data)
	return state, nil
}

func toCKBAllocation(protoAlloc *protobuf.Allocation) (*channel.Allocation, error) {
	alloc := &channel.Allocation{}
	alloc.Assets = make([]channel.Asset, len(protoAlloc.Assets))
	for i := range protoAlloc.Assets {
		// NOTE: We will assume the first asset will always be CKBytes.
		if i == 0 {
			alloc.Assets[i] = asset.NewCKBytesAsset()
		} else {
			alloc.Assets[i] = channel.NewAsset()
		}
		err := alloc.Assets[i].UnmarshalBinary(protoAlloc.Assets[i])
		if err != nil {
			return nil, fmt.Errorf("%d'th asset: %w", i, err)
		}
	}
	alloc.Locked = make([]channel.SubAlloc, len(protoAlloc.Locked))
	for i := range protoAlloc.Locked {
		locked, err := protobuf.ToSubAlloc(protoAlloc.Locked[i])
		if err != nil {
			return nil, fmt.Errorf("%d'th sub alloc: %w", i, err)
		}
		alloc.Locked[i] = locked
	}
	alloc.Balances = protobuf.ToBalances(protoAlloc.Balances)

	return alloc, nil
}

// toAppAndData converts protobuf app and data to a channel.App and channel.Data.
func toAppAndData(protoApp, protoData []byte) (app channel.App, data channel.Data, err error) {
	if len(protoApp) == 0 {
		app = channel.NoApp()
		data = channel.NoData()
		return app, data, nil
	}
	appDef := channel.NewAppID()
	err = appDef.UnmarshalBinary(protoApp)
	if err != nil {
		return nil, nil, err
	}
	app, err = channel.Resolve(appDef)
	if err != nil {
		return
	}
	data = app.NewData()
	return app, data, data.UnmarshalBinary(protoData)
}
