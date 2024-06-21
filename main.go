package main

import (
	"context"
	"errors"
	"log"
	"os"
	"time"

	"google.golang.org/protobuf/types/known/emptypb"
	"polycry.pt/poly-go/sync"

	"github.com/nervosnetwork/ckb-sdk-go/v2/types"
	"perun.network/go-perun/channel"
	gpwallet "perun.network/go-perun/wallet"
	"perun.network/perun-ckb-backend/channel/asset"
	"perun.network/perun-ckb-backend/wallet"
	vc "perun.network/perun-demo-tui/client"
	"perun.network/perun-demo-tui/view"
	"perun.network/perun-nervos-demo/client"
	"perun.network/perun-nervos-demo/deployment"
)

const (
	rpcNodeURL = "http://localhost:8114"
	network    = types.NetworkTest
	aliceWSURL = "localhost:50051"
	bobWSURL   = "localhost:50052"
	aliceCSURL = "localhost:4321"
	bobCSURL   = "localhost:4322"
)

func SetLogFile(path string) {
	logFile, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
	if err != nil {
		log.Fatalf("error opening file: %v", err)
	}
	log.SetOutput(logFile)
}

type AssetRegister struct {
	getName  map[channel.Asset]string
	getAsset map[string]channel.Asset
	assets   []channel.Asset
}

func (a AssetRegister) GetAsset(name string) channel.Asset {
	return a.getAsset[name]
}

func (a AssetRegister) GetName(asset channel.Asset) string {
	return a.getName[asset]
}

func (a AssetRegister) GetAllAssets() []channel.Asset {
	return a.assets
}

func newAssetRegister(assets []channel.Asset, names []string) (*AssetRegister, error) {
	assetRegister := &AssetRegister{
		getName:  make(map[channel.Asset]string),
		getAsset: make(map[string]channel.Asset),
		assets:   assets,
	}
	if len(assets) != len(names) {
		return nil, errors.New("length of assets and names must be equal")
	}
	for i, a := range assets {
		if a == nil {
			return nil, errors.New("asset cannot be nil")
		}
		if names[i] == "" {
			return nil, errors.New("name cannot be empty")
		}
		if assetRegister.getName[a] != "" {
			return nil, errors.New("duplicate asset")
		}
		if assetRegister.getAsset[names[i]] != nil {
			return nil, errors.New("duplicate name")
		}
		assetRegister.getName[a] = names[i]
		assetRegister.getAsset[names[i]] = a
	}
	return assetRegister, nil
}

func main() {
	SetLogFile("demo.log")

	ckbAsset := asset.NewCKBytesAsset()

	assetRegister, err := newAssetRegister([]channel.Asset{ckbAsset}, []string{"CKBytes"})
	if err != nil {
		log.Fatalf("error creating mapping: %v", err)
	}

	keyAlice, err := deployment.GetKey("./devnet/accounts/alice.pk")
	if err != nil {
		log.Fatalf("error getting alice's private key: %v", err)
	}
	keyBob, err := deployment.GetKey("./devnet/accounts/bob.pk")
	if err != nil {
		log.Fatalf("error getting bob's private key: %v", err)
	}
	aliceAccount := wallet.NewAccountFromPrivateKey(keyAlice)
	bobAccount := wallet.NewAccountFromPrivateKey(keyBob)

	parties := []gpwallet.Address{aliceAccount.Address(), bobAccount.Address()}

	// Create a wait group
	var wg sync.WaitGroup
	// Setup clients
	log.Println("Setting up clients.")
	alice, err := client.NewWalletClient(
		"Alice",
		network,
		rpcNodeURL,
		parties,
		[]channel.Asset{ckbAsset},
		aliceWSURL,
		aliceCSURL,
		aliceAccount,
		keyAlice,
		assetRegister,
		&wg,
	)
	if err != nil {
		log.Fatalf("error creating alice's client: %v", err)
	}
	bob, err := client.NewWalletClient(
		"Bob",
		network,
		rpcNodeURL,
		parties,
		[]channel.Asset{ckbAsset},
		bobWSURL,
		bobCSURL,
		bobAccount,
		keyBob,
		assetRegister,
		&wg,
	)
	if err != nil {
		log.Fatalf("error creating bob's client: %v", err)
	}
	// Handle termination signal in a separate goroutine
	defer func() {
		log.Println("Main process received shutdown signal")

		// Shutdown wallet services
		alice.WalletServer.Shutdown(&wg)
		bob.WalletServer.Shutdown(&wg)

		// Close channel services' perun clients
		_, err = alice.ChannelService.ClosePerunClient(context.Background(), &emptypb.Empty{})
		if err != nil {
			log.Printf("error closing alice's perun client: %v", err)
		}
		_, err = bob.ChannelService.ClosePerunClient(context.Background(), &emptypb.Empty{})
		if err != nil {
			log.Printf("error closing bob's perun client: %v", err)
		}
		// Wait for all wallet services to shut down
		wg.Wait()

		log.Println("Main process exiting")
		os.Exit(0)
	}()
	time.Sleep(2 * time.Second) // Wait for clients to connect with channel service
	clients := []vc.DemoClient{alice, bob}
	_ = view.RunDemo("Perun Nervos Channel Service Demo", clients, assetRegister)

}
