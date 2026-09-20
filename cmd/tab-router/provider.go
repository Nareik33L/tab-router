package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/Nareik33L/tab-router/controller/config"
	"github.com/Nareik33L/tab-router/routing/manager"
)

func runProvider(args []string) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprintln(os.Stderr, "Usage:")
		fmt.Fprintln(os.Stderr, "  tab-router provider login [--account N] [--identities 2] [--data-dir DIR]")
		fmt.Fprintln(os.Stderr, "  tab-router provider status [--data-dir DIR]")
		fmt.Fprintln(os.Stderr, "  tab-router provider logout [--data-dir DIR]")
		return exitUsage
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "login":
		return providerLogin(rest)
	case "status":
		return providerStatus(rest)
	case "logout":
		return providerLogout(rest)
	default:
		fmt.Fprintf(os.Stderr, "unknown provider command %q\n", cmd)
		return exitUsage
	}
}

func providerFlags(name string, args []string) (dataDir, account string, n int, ok bool) {
	fs := flag.NewFlagSet("tab-router provider "+name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dd := fs.String("data-dir", "", "data directory (default: per-user application data)")
	acc := fs.String("account", "", "Mullvad account number (or TAB_ROUTER_MULLVAD_ACCOUNT)")
	ids := fs.Int("identities", config.MaxIdentities, "devices to register (one per identity slot)")
	if err := fs.Parse(args); err != nil {
		return "", "", 0, false
	}
	return *dd, *acc, *ids, true
}

func providerDataDir(explicit string) (string, bool) {
	cfg, err := config.Load(config.Overrides{DataDir: explicit})
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return "", false
	}
	return cfg.DataDir, true
}

func providerLogin(args []string) int {
	dataDir, account, n, ok := providerFlags("login", args)
	if !ok {
		return exitUsage
	}
	if n < 1 || n > config.MaxIdentities {
		fmt.Fprintf(os.Stderr, "identities must be 1..%d\n", config.MaxIdentities)
		return exitUsage
	}
	dir, ok := providerDataDir(dataDir)
	if !ok {
		return exitUsage
	}
	if account == "" {
		account = os.Getenv("TAB_ROUTER_MULLVAD_ACCOUNT")
	}
	if account == "" {
		if !isTerminal(os.Stdin) {
			fmt.Fprintln(os.Stderr, "pass --account or TAB_ROUTER_MULLVAD_ACCOUNT (stdin is not a terminal)")
			return exitUsage
		}
		fmt.Print("Mullvad account number: ")
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		account = strings.TrimSpace(line)
	}
	ctx, cancel := context.WithTimeout(context.Background(), manager.LoginTimeout)
	defer cancel()
	path := manager.Path(dir)
	if err := manager.Login(ctx, path, account, n); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return exitRoute
	}
	f, err := manager.LoadFile(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return exitUsage
	}
	fmt.Printf("Provider: mullvad\n")
	fmt.Printf("Stored owner-only in %s\n", path)
	fmt.Printf("Account: %s\n", manager.RedactAccount(f.Account))
	for _, d := range f.Device {
		fmt.Printf("Device slot %d: %s (%s)\n", d.Slot, d.Name, d.IPv4)
	}
	fmt.Println()
	fmt.Println("Start with:  tab-router --identities 2 --url https://example.com")
	return exitOK
}

func providerStatus(args []string) int {
	dataDir, _, _, ok := providerFlags("status", args)
	if !ok {
		return exitUsage
	}
	dir, ok := providerDataDir(dataDir)
	if !ok {
		return exitUsage
	}
	path := manager.Path(dir)
	f, err := manager.LoadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Print(manager.MissingProviderMessage(dir))
			return exitUsage
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		return exitUsage
	}
	fmt.Printf("Provider: %s\n", f.Type)
	fmt.Printf("File: %s\n", path)
	fmt.Printf("Account: %s\n", manager.RedactAccount(f.Account))
	if len(f.Device) == 0 {
		fmt.Println("Devices: none yet (they are registered on first start)")
		return exitOK
	}
	fmt.Printf("Devices: %d\n", len(f.Device))
	for _, d := range f.Device {
		fmt.Printf("  slot %d  %s  %s\n", d.Slot, d.Name, d.IPv4)
	}
	return exitOK
}

func providerLogout(args []string) int {
	dataDir, _, _, ok := providerFlags("logout", args)
	if !ok {
		return exitUsage
	}
	dir, ok := providerDataDir(dataDir)
	if !ok {
		return exitUsage
	}
	path := manager.Path(dir)
	if err := manager.Logout(path); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return exitUsage
	}
	fmt.Println("Removed local provider configuration.")
	fmt.Println("Mullvad devices were not deleted; manage them at https://mullvad.net/account")
	fmt.Println("--fresh never does this.")
	return exitOK
}
