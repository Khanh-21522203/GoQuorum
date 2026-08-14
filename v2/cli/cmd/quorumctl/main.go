// Command quorumctl is GoQuorum v2's control CLI. It is a thin flag/dispatch
// layer: all subcommand behaviour lives in cli/internal/command.
//
// Usage: quorumctl [--addr host:port] <command> [args...]
//
// Commands:
//
//	get <key>                     Get value for key
//	put <key> <value>             Put value for key
//	delete <key> <context>        Delete key (context is base64-encoded JSON vclock)
//	status                        Show cluster health
//	ring                          Show cluster info
//	key-info <key>                Show replica info for key
//	compact                       Trigger storage compaction
//
// (v1: cmd/quorumctl/main.go)
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"goquorum.io/v2/cli/internal/command"
)

const usage = `Usage: quorumctl [--addr host:port] <command> [args...]

Commands:
  get <key>                     Get value for key
  put <key> <value>             Put value for key
  delete <key> <context>        Delete key (context is base64-encoded JSON vclock)
  status                        Show cluster health
  ring                          Show cluster info
  key-info <key>                Show replica info for key
  compact                       Trigger storage compaction
`

// dispatch maps each subcommand name to its cli/internal/command
// implementation. get/put/delete talk to a running node over the client
// library; status/ring/key-info/compact are stubs (see
// cli/internal/command/admin.go).
var dispatch = map[string]command.Func{
	"get":      command.Get,
	"put":      command.Put,
	"delete":   command.Delete,
	"status":   command.Status,
	"ring":     command.Ring,
	"key-info": command.KeyInfo,
	"compact":  command.Compact,
}

func main() {
	addr := flag.String("addr", "localhost:7070", "GoQuorum server address (host:port)")
	flag.Usage = func() {
		fmt.Fprint(os.Stderr, usage)
		flag.PrintDefaults()
	}
	flag.Parse()

	args := flag.Args()
	if len(args) == 0 {
		flag.Usage()
		os.Exit(1)
	}

	name, cmdArgs := args[0], args[1:]

	fn, ok := dispatch[name]
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", name)
		flag.Usage()
		os.Exit(1)
	}

	if err := fn(context.Background(), *addr, cmdArgs, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", name, err)
		os.Exit(1)
	}
}
