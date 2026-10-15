package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"fcard/internal/deposit"

	"github.com/joho/godotenv"
)

func main() {
	probe := flag.Bool("rpc", false, "explicitly probe read-only Nile identity and checkpoint; never create a gateway order")
	flag.Parse()
	if err := run(*probe); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(probe bool) error {
	if err := godotenv.Load(".env.deposits"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.New("cannot load private deposit configuration")
	}
	settings, err := deposit.SettingsFromEnvironment()
	if err != nil {
		return err
	}
	if !probe {
		fmt.Println("deposit configuration parsed; no network requests or database writes")
		if !settings.RuntimeEnabled {
			fmt.Println("deposit runtime and creation remain disabled; reviewed Nile configuration is not yet complete")
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	result, err := deposit.ProbeNile(ctx, settings)
	if err != nil {
		return errors.New("Nile read-only preflight failed; check chain identity, freshness and reviewed checkpoint")
	}
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		return err
	}
	fmt.Println("identity/checkpoint only; token bytecode, six decimals, wallet ownership and gateway HTTPS still require review before payment")
	return nil
}
