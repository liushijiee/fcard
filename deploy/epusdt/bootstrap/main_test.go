package main

import "testing"

func TestAllowedRPC(t *testing.T) {
	for _, endpoint := range []string{"https://nile.trongrid.io", "https://nile.trongrid.io/"} {
		if !allowedRPC(endpoint) {
			t.Errorf("rejected Nile endpoint %s", endpoint)
		}
	}
	for _, endpoint := range []string{"https://api.trongrid.io", "http://nile.trongrid.io", "https://nile.trongrid.io.evil.test", "https://user@nile.trongrid.io", "https://nile.trongrid.io:443", "https://nile.trongrid.io/mainnet", "https://nile.trongrid.io?target=mainnet", "https://nile.trongrid.io#mainnet"} {
		if allowedRPC(endpoint) {
			t.Errorf("accepted unsafe endpoint %s", endpoint)
		}
	}
}
