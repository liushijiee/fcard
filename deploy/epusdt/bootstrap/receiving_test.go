package main

import (
	"testing"
	"time"
)

func TestReceivingPolicy(t *testing.T) {
	valid := receivingPolicy{Environment: "nile", Genesis: nileGenesis, Contract: "TXYZopYRdj2D9XRtbG411XZZ3kM5VkAeBf", Decimals: 6, BytecodeSHA256: "2866816060782939f36b0f42dd695c197b72d4ea133f433a08575c9a528a93b5", Wallets: []string{"TXYZopYRdj2D9XRtbG411XZZ3kM5VkAeBf"}, CheckoutOrigin: "https://checkout.example", ReviewedAt: time.Now().UTC()}
	if err := valid.validate(); err != nil {
		t.Fatal(err)
	}
	for _, change := range []struct {
		name   string
		mutate func(*receivingPolicy)
	}{
		{"mainnet", func(p *receivingPolicy) { p.Environment = "mainnet" }},
		{"wrong genesis", func(p *receivingPolicy) { p.Genesis = "other" }},
		{"bad contract checksum", func(p *receivingPolicy) { p.Contract = "TXYZopYRdj2D9XRtbG411XZZ3kM5VkAeBg" }},
		{"wrong precision", func(p *receivingPolicy) { p.Decimals = 18 }},
		{"bad wallet", func(p *receivingPolicy) { p.Wallets = []string{"invalid"} }},
		{"duplicate wallet", func(p *receivingPolicy) { p.Wallets = append(p.Wallets, p.Wallets[0]) }},
		{"plaintext checkout", func(p *receivingPolicy) { p.CheckoutOrigin = "http://checkout.example" }},
		{"checkout prefix", func(p *receivingPolicy) { p.CheckoutOrigin += "/epusdt" }},
		{"bad hash", func(p *receivingPolicy) { p.BytecodeSHA256 = "bad" }},
	} {
		t.Run(change.name, func(t *testing.T) {
			p := valid
			change.mutate(&p)
			if p.validate() == nil {
				t.Fatal("invalid policy accepted")
			}
		})
	}
}
