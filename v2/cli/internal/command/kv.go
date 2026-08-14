package command

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"goquorum.io/v2/client"
	"goquorum.io/v2/contracts/vclock"
)

// requestTimeout bounds each RPC issued by the kv subcommands.
//
// (v1: cmd/quorumctl/main.go runGet/runPut/runDelete, 10*time.Second)
const requestTimeout = 10 * time.Second

// Func is the signature every quorumctl subcommand implementation
// satisfies: addr is the --addr flag value, args are the subcommand's
// positional arguments, and out receives the command's human-readable
// output.
type Func func(ctx context.Context, addr string, args []string, out io.Writer) error

// Get retrieves all sibling values for a key.
//
// TODO(v2): once client.Client.Get is implemented against the real
// transport, this will actually return siblings instead of
// contracts.ErrNotImplemented (v1: cmd/quorumctl/main.go runGet).
func Get(ctx context.Context, addr string, args []string, out io.Writer) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: get <key>")
	}
	key := args[0]

	c, err := client.NewClient(client.DefaultClientConfig(addr))
	if err != nil {
		return fmt.Errorf("connect to %s: %w", addr, err)
	}
	defer c.Close()

	reqCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	siblings, err := c.Get(reqCtx, []byte(key))
	if err != nil {
		return fmt.Errorf("get failed: %w", err)
	}

	if len(siblings) == 0 {
		fmt.Fprintln(out, "(no values found)")
		return nil
	}

	for i, sib := range siblings {
		ctxB64, err := vclockToBase64(sib.Context)
		if err != nil {
			return fmt.Errorf("encode context: %w", err)
		}
		fmt.Fprintf(out, "sibling[%d]:\n", i)
		fmt.Fprintf(out, "  value:   %s\n", string(sib.Value))
		fmt.Fprintf(out, "  context: %s\n", ctxB64)
		fmt.Fprintf(out, "  ts:      %d\n", sib.Timestamp)
		if sib.Tombstone {
			fmt.Fprintf(out, "  tombstone: true\n")
		}
	}
	return nil
}

// Put stores value for key with a blind write (empty causal context).
//
// TODO(v2): accept a --context flag to carry a prior Get's context forward
// instead of always writing blind, once client.Client.Put is implemented
// (v1: cmd/quorumctl/main.go runPut).
func Put(ctx context.Context, addr string, args []string, out io.Writer) error {
	if len(args) != 2 {
		return fmt.Errorf("usage: put <key> <value>")
	}
	key, value := args[0], args[1]

	c, err := client.NewClient(client.DefaultClientConfig(addr))
	if err != nil {
		return fmt.Errorf("connect to %s: %w", addr, err)
	}
	defer c.Close()

	reqCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	newVC, err := c.Put(reqCtx, []byte(key), []byte(value), vclock.NewVectorClock())
	if err != nil {
		return fmt.Errorf("put failed: %w", err)
	}

	ctxB64, err := vclockToBase64(newVC)
	if err != nil {
		return fmt.Errorf("encode context: %w", err)
	}
	fmt.Fprintf(out, "ok\ncontext: %s\n", ctxB64)
	return nil
}

// Delete removes key by writing a tombstone. args[1] is the base64-encoded
// JSON vclock returned by a prior Get or Put.
//
// TODO(v2): once client.Client.Delete is implemented against the real
// transport, this will actually delete the key (v1: cmd/quorumctl/main.go
// runDelete).
func Delete(ctx context.Context, addr string, args []string, out io.Writer) error {
	if len(args) != 2 {
		return fmt.Errorf("usage: delete <key> <context>")
	}
	key, ctxArg := args[0], args[1]

	vc, err := base64ToVClock(ctxArg)
	if err != nil {
		return fmt.Errorf("decode context: %w", err)
	}

	c, err := client.NewClient(client.DefaultClientConfig(addr))
	if err != nil {
		return fmt.Errorf("connect to %s: %w", addr, err)
	}
	defer c.Close()

	reqCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	if err := c.Delete(reqCtx, []byte(key), vc); err != nil {
		return fmt.Errorf("delete failed: %w", err)
	}
	fmt.Fprintln(out, "ok")
	return nil
}

// vclockToBase64 serialises a VectorClock as base64(JSON).
//
// (v1: cmd/quorumctl/main.go vclockToBase64)
func vclockToBase64(vc vclock.VectorClock) (string, error) {
	data, err := json.Marshal(vc)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(data), nil
}

// base64ToVClock deserialises a VectorClock from base64(JSON).
//
// (v1: cmd/quorumctl/main.go base64ToVClock)
func base64ToVClock(encoded string) (vclock.VectorClock, error) {
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return vclock.VectorClock{}, fmt.Errorf("base64 decode: %w", err)
	}
	var vc vclock.VectorClock
	if err := json.Unmarshal(data, &vc); err != nil {
		return vclock.VectorClock{}, fmt.Errorf("json unmarshal: %w", err)
	}
	return vc, nil
}
