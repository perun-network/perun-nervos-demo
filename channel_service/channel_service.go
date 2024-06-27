package main

import (
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/nervosnetwork/ckb-sdk-go/v2/types"
	"google.golang.org/grpc"
	"perun.network/channel-service/rpc/proto"
	"perun.network/channel-service/service"
	"perun.network/channel-service/wallet"
	"perun.network/perun-ckb-backend/backend"
	"perun.network/perun-ckb-backend/wallet/address"
	"perun.network/perun-ckb-backend/wallet/external"
	"perun.network/perun-nervos-demo/deployment"
	"polycry.pt/poly-go/sortedkv/leveldb"
)

const (
	rpcNodeURL  = "http://localhost:8114"
	network     = types.NetworkTest
	hostA       = "localhost:4321"
	hostB       = "localhost:4322"
	aliceWSSURL = "localhost:50051"
	bobWSSURL   = "localhost:50052"
)

// SetLogFile sets the log file for the channel service.
func SetLogFile(path string) {
	logFile, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
	if err != nil {
		log.Fatalf("error opening file: %v", err)
	}
	log.SetOutput(logFile)
}

func parseSUDTOwnerLockArg(pathToSUDTOwnerLockArg string) (string, error) {
	b, err := os.ReadFile(pathToSUDTOwnerLockArg)
	if err != nil {
		return "", fmt.Errorf("reading sudt owner lock arg from file: %w", err)
	}
	sudtOwnerLockArg := string(b)
	if sudtOwnerLockArg == "" {
		return "", errors.New("sudt owner lock arg not found in file")
	}
	return sudtOwnerLockArg, nil
}

// MakeDeployment creates a deployment object.
func MakeDeployment() (backend.Deployment, error) {
	sudtOwnerLockArg, err := parseSUDTOwnerLockArg("../devnet/accounts/sudt-owner-lock-hash.txt")
	if err != nil {
		log.Fatalf("error getting SUDT owner lock arg: %v", err)
	}
	d, _, err := deployment.GetDeployment("../devnet/contracts/migrations/dev/", "../devnet/system_scripts", sudtOwnerLockArg)
	return d, err
}

// MakeParticipants creates participants from public keys.
func MakeParticipants(pks []secp256k1.PublicKey) ([]address.Participant, error) {
	parts := make([]address.Participant, len(pks))
	for i := range pks {
		part, err := address.NewDefaultParticipant(&pks[i])
		if err != nil {
			return nil, fmt.Errorf("unable to create participant: %w", err)
		}
		parts[i] = *part
	}
	return parts, nil
}

// Start channel service GRPC server.
func main() {
	SetLogFile("channel_service.log")

	// Set up ChannelService
	d, err := MakeDeployment()
	if err != nil {
		log.Fatalf("error getting deployment: %v", err)
	}

	keyAlice, err := deployment.GetKey("../devnet/accounts/alice.pk")
	if err != nil {
		log.Fatalf("error getting alice's private key: %v", err)
	}
	keyBob, err := deployment.GetKey("../devnet/accounts/bob.pk")
	if err != nil {
		log.Fatalf("error getting bob's private key: %v", err)
	}

	pubKeys := make([]secp256k1.PublicKey, 2)
	pubKeys[0] = *keyAlice.PubKey()
	pubKeys[1] = *keyBob.PubKey()

	parts, err := MakeParticipants(pubKeys)
	if err != nil {
		log.Fatalf("error making participants: %v", err)
	}

	aliceWSC := setupWalletServiceClient(aliceWSSURL)
	bobWSC := setupWalletServiceClient(bobWSSURL)

	// Setup Alice
	dbAlice, err := leveldb.LoadDatabase("./alice-db")
	if err != nil {
		log.Fatalf("loading database: %v", err)
	}

	csA, err := service.NewChannelService(aliceWSC, network, rpcNodeURL, d, nil, dbAlice)
	if err != nil {
		log.Fatalf("creating channel service: %v", err)
	}

	// Setup Bob
	dbBob, err := leveldb.LoadDatabase("./bob-db")
	if err != nil {
		log.Fatalf("loading database: %v", err)
	}

	csB, err := service.NewChannelService(bobWSC, network, rpcNodeURL, d, nil, dbBob)
	if err != nil {
		log.Fatalf("creating channel service: %v", err)
	}

	lisA, err := net.Listen("tcp", hostA)
	if err != nil {
		log.Fatalf("failed to listen: %v", err)
	}

	lisB, err := net.Listen("tcp", hostB)
	if err != nil {
		log.Fatalf("failed to listen: %v", err)
	}
	var opts []grpc.ServerOption
	sA := grpc.NewServer(opts...)
	proto.RegisterChannelServiceServer(sA, csA)

	sB := grpc.NewServer(opts...)
	proto.RegisterChannelServiceServer(sB, csB)

	// Signal handling for graceful shutdown
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)

	// Channel to notify when servers are stopped
	done := make(chan bool, 1)

	// Handle termination signal in a separate goroutine
	go func() {
		<-sigs
		fmt.Println("Shutting down gRPC servers...")

		// Graceful stop
		sA.Stop()
		sB.Stop()

		fmt.Println("gRPC servers stopped.")
		done <- true
	}()

	// Start the servers
	go func() {
		fmt.Printf("Starting Alice Channel Service Server at %s \n", hostA)
		err = sA.Serve(lisA)
		if err != nil {
			log.Fatalf("serving channel service: %v", err)
		}
	}()

	go func() {
		fmt.Printf("Starting Bob Channel Service Server at %s \n", hostB)
		err = sB.Serve(lisB)
		if err != nil {
			log.Fatalf("serving channel service: %v", err)
		}
	}()

	log.Printf("Participants: %v", parts)
	// Initialize Users
	for i, part := range parts {
		if i == 0 {
			_, err = csA.InitializeUser(part, aliceWSC, external.NewWallet(wallet.NewExternalClient(aliceWSC)))
			if err != nil {
				log.Fatalf("error initializing user A: %v", err)
			}
		} else {
			_, err = csB.InitializeUser(part, bobWSC, external.NewWallet(wallet.NewExternalClient(bobWSC)))
			if err != nil {
				log.Fatalf("error initializing user B: %v", err)
			}
		}
		if err != nil {
			log.Fatalf("error initializing user: %v", err)
		}
	}

	// Wait for the servers to stop
	<-done
}
