package deposit

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestNileSolidRPCContract(t *testing.T) {
	if os.Getenv("FCARD_TEST_NILE_RPC") != "1" {
		t.Skip("set FCARD_TEST_NILE_RPC=1 for read-only official Nile Solidity RPC probes")
	}
	rpc, err := NewTronRPC(TronRPCConfig{Environment: TronEnvironmentNile, BaseURL: "https://nile.trongrid.io"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	genesis, err := rpc.GenesisID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if genesis != "0000000000000000d698d4192c56cb6be724a558448e2684802de4d6cd8690dc" {
		t.Fatal("Nile genesis differs from the recorded 2026-10-13 observation; review before changing the pin")
	}
	head, err := rpc.SolidHead(ctx)
	if err != nil {
		t.Fatal(err)
	}
	height, err := tronBlockHeight(head)
	if err != nil || height <= 0 {
		t.Fatal("invalid Nile solid head")
	}
	block, err := rpc.SolidBlock(ctx, height)
	if err != nil || !strings.EqualFold(block.ID, head.ID) {
		t.Fatal("solid block query does not match the observed head")
	}
	t.Logf("read-only Nile RPC contract passed: solid height %d; no token lookup or transfer", height)
}

func TestNileNativeReceiptContract(t *testing.T) {
	if os.Getenv("FCARD_TEST_NILE_RPC") != "1" {
		t.Skip("set FCARD_TEST_NILE_RPC=1 for read-only public Nile native receipt verification")
	}
	rpc, err := NewTronRPC(TronRPCConfig{Environment: TronEnvironmentNile, BaseURL: "https://nile.trongrid.io"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	genesis, err := rpc.GenesisID(ctx)
	if err != nil || genesis != "0000000000000000d698d4192c56cb6be724a558448e2684802de4d6cd8690dc" {
		t.Fatal("Nile source identity verification failed")
	}
	block, err := rpc.SolidBlock(ctx, 71782721)
	if err != nil || block.ID != "00000000044751412dfd16b552f057c096f48ecec4a042f3b7ecb928220439cb" {
		t.Fatal("recorded public solid block is unavailable or differs")
	}
	for _, transaction := range block.Transactions {
		if transaction.TxID != "5cbde0e6523e47dd14a1cae459ee7b1d57f8aaf92e1bd1ade208921e93a51761" {
			continue
		}
		receipt, err := rpc.SolidReceipt(ctx, transaction.TxID)
		if err != nil {
			t.Fatal(err)
		}
		evidence, err := scanTransactionEvidence(TronVerifierConfig{Environment: TronEnvironmentNile, GenesisID: genesis}, block, transaction, receipt)
		if err != nil || evidence.Disposition != tronNativeSuccess || len(evidence.Transfers) != 0 {
			t.Fatal("public native receipt does not satisfy the explicit no-TRC20 evidence policy", err)
		}
		t.Log("read-only recorded native transaction verified; no configured token, user wallet or transfer")
		return
	}
	t.Fatal("recorded public transaction is missing from its solid block")
}
