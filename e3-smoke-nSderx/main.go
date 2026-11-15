package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/config"
	"github.com/A2MaTex/a2matex-runtime/internal/platform/adb"
)

func main() {
	cfg, err := config.Load(config.Overrides{})
	if err != nil {
		log.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	resolver := adb.NewResolver(cfg.ADB)
	toolchain, err := resolver.Resolve(ctx)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("ADB %s (%s): %s\n", toolchain.Version, toolchain.Source, toolchain.Executable)

	endpoints, err := resolver.Discover(ctx)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Discovered %d endpoint(s)\n", len(endpoints))
	for _, endpoint := range endpoints {
		fmt.Printf("%s %s\n", endpoint.ADBSerial, endpoint.State)
	}
}
