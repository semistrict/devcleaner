package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"devcleaner/internal/apphost"
	"devcleaner/internal/command"
)

func forwardedArgs(args []string) ([]string, error) {
	result := append([]string(nil), args...)
	explicitDB := false
	for i := 0; i < len(result); i++ {
		arg := result[i]
		for _, flag := range []string{"--root", "--path", "--db", "-root", "-path", "-db"} {
			if arg == flag {
				if flag == "--db" || flag == "-db" {
					explicitDB = true
				}
				if i+1 < len(result) {
					i++
					p, err := filepath.Abs(result[i])
					if err != nil {
						return nil, err
					}
					result[i] = p
				}
				break
			}
			if strings.HasPrefix(arg, flag+"=") {
				if flag == "--db" || flag == "-db" {
					explicitDB = true
				}
				p, err := filepath.Abs(strings.TrimPrefix(arg, flag+"="))
				if err != nil {
					return nil, err
				}
				result[i] = flag + "=" + p
				break
			}
		}
	}
	if !explicitDB {
		if db := os.Getenv("DEVCLEANER_DB"); db != "" {
			p, err := filepath.Abs(db)
			if err != nil {
				return nil, err
			}
			result = append(result, "--db", p)
		}
	}
	return result, nil
}

func execute(ctx context.Context, args []string) int {
	requireApp, standalone := false, false
	filtered := []string{}
	for _, arg := range args {
		switch arg {
		case "--app":
			requireApp = true
		case "--standalone":
			standalone = true
		default:
			filtered = append(filtered, arg)
		}
	}
	if requireApp && standalone {
		fmt.Fprintln(os.Stdout, "Error: --app and --standalone cannot be combined.")
		return 2
	}
	if !standalone && len(filtered) > 0 && filtered[0] != "help" && filtered[0] != "--help" && filtered[0] != "-h" && filtered[0] != "version" {
		forwarded, err := forwardedArgs(filtered)
		if err != nil {
			fmt.Fprintf(os.Stdout, "Error: %v\n", err)
			return 1
		}
		handled, code, err := apphost.TryRun(ctx, forwarded, os.Stdout, os.Stderr)
		if err != nil {
			fmt.Fprintf(os.Stdout, "Error: %v\n", err)
			return 1
		}
		if handled {
			return code
		}
		if requireApp {
			fmt.Fprintln(os.Stdout, "Error: DevCleaner.app is not running. Open the app from Finder, then retry. No scan was run from this terminal.")
			return 1
		}
	}
	return command.Run(ctx, filtered, os.Stdout, os.Stderr)
}
func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	os.Exit(execute(ctx, os.Args[1:]))
}
