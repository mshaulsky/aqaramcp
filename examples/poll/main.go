// Command poll explores an Aqara Home account through the Aqara MCP server:
// it lists devices, prints their current state, and — for tools this package
// does not wrap — shows what the server offers and calls it raw.
//
// The API key comes from signing in at https://agent.aqara.com/login:
//
//	export AQARA_MCP_KEY=...
//
//	go run ./examples/poll -devices
//	go run ./examples/poll -status
//	go run ./examples/poll -status -watch 2m
//
//	go run ./examples/poll -tools
//	go run ./examples/poll -schema device_status_inquiry
//	go run ./examples/poll -call device_status_inquiry -args '{"device_types":["DoorLock"]}'
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sort"
	"strings"
	"time"

	"github.com/mshaulsky/aqaramcp"
)

func main() {
	log.SetFlags(log.Ltime)
	log.SetPrefix("poll: ")

	if err := execute(); err != nil {
		log.Fatal(err)
	}
}

// execute parses the flags, builds the client and runs the command. It reports
// its error rather than exiting, so that deferred cleanup still runs.
func execute() error {
	var (
		devices = flag.Bool("devices", false, "list the devices of the account")
		status  = flag.Bool("status", false, "print the current state of every device")
		watch   = flag.Duration("watch", 0, "repeat -status at this interval")
		tools   = flag.Bool("tools", false, "list the tools the server offers")
		schema  = flag.String("schema", "", "print the input schema of one tool")
		call    = flag.String("call", "", "call a tool by name and print the raw answer")
		args    = flag.String("args", "", "JSON arguments for -call")
	)
	flag.Parse()

	key := os.Getenv("AQARA_MCP_KEY")
	if key == "" {
		return errors.New("set AQARA_MCP_KEY to the key from agent.aqara.com")
	}
	var opts []aqaramcp.Option
	if endpoint := os.Getenv("AQARA_MCP_URL"); endpoint != "" {
		opts = append(opts, aqaramcp.WithEndpoint(endpoint))
	}
	client, err := aqaramcp.New(key, opts...)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	switch {
	case *devices:
		return listDevices(ctx, client)
	case *status:
		return repeat(ctx, *watch, func(ctx context.Context) error { return showStatus(ctx, client) })
	case *tools:
		return listTools(ctx, client)
	case *schema != "":
		return printSchema(ctx, client, *schema)
	case *call != "":
		return callTool(ctx, client, *call, *args)
	default:
		flag.Usage()
		return errors.New("nothing to do")
	}
}

// repeat runs once, or every interval until interrupted.
func repeat(ctx context.Context, interval time.Duration, once func(context.Context) error) error {
	for {
		if err := once(ctx); err != nil {
			return err
		}
		if interval <= 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(interval):
		}
	}
}

// listDevices prints every device, grouped by room.
func listDevices(ctx context.Context, client *aqaramcp.Client) error {
	devices, err := client.Devices(ctx, aqaramcp.DeviceFilter{})
	if err != nil {
		return err
	}
	sort.SliceStable(devices, func(i, j int) bool {
		return devices[i].PositionName < devices[j].PositionName
	})
	log.Printf("%d device(s):", len(devices))
	for _, d := range devices {
		fmt.Printf("  %-40s %-18s %-22s %s\n", d.EndpointID, d.Type, d.PositionName, d.DeviceName)
	}
	return nil
}

// showStatus prints the current state of every device.
func showStatus(ctx context.Context, client *aqaramcp.Client) error {
	statuses, err := client.Statuses(ctx, aqaramcp.StatusFilter{})
	if err != nil {
		return err
	}
	log.Printf("%d device(s):", len(statuses))
	for _, s := range statuses {
		state := "online"
		if !s.Status.Online() {
			state = "OFFLINE"
		}
		fmt.Printf("  %-22s %-18s %-8s %s\n", s.DeviceName, s.Type, state, describe(s.Status))
	}
	return nil
}

// describe renders a status without its online flag, keys sorted.
func describe(status aqaramcp.Status) string {
	keys := make([]string, 0, len(status))
	for k := range status {
		if k != aqaramcp.KeyOnline {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+status[k])
	}
	return strings.Join(parts, " ")
}

// listTools prints every tool with the first line of its description.
func listTools(ctx context.Context, client *aqaramcp.Client) error {
	tools, err := client.Tools(ctx)
	if err != nil {
		return err
	}
	log.Printf("%d tool(s):", len(tools))
	for _, tool := range tools {
		fmt.Printf("  %-44s %s\n", tool.Name, firstLine(tool.Description))
	}
	return nil
}

// printSchema prints one tool's description and input schema.
func printSchema(ctx context.Context, client *aqaramcp.Client, name string) error {
	tools, err := client.Tools(ctx)
	if err != nil {
		return err
	}
	for _, tool := range tools {
		if tool.Name != name {
			continue
		}
		fmt.Println(tool.Description)
		fmt.Println(pretty(tool.InputSchema))
		return nil
	}
	return fmt.Errorf("the server offers no tool named %s", name)
}

// callTool runs one tool and prints everything it answered.
func callTool(ctx context.Context, client *aqaramcp.Client, name, rawArgs string) error {
	var args any
	if rawArgs != "" {
		if err := json.Unmarshal([]byte(rawArgs), &args); err != nil {
			return fmt.Errorf("-args is not valid JSON: %w", err)
		}
	}
	result, err := client.Call(ctx, name, args)
	if err != nil {
		return err
	}
	if result.IsError {
		log.Print("the tool reported an error:")
	}
	if text := result.Text(); text != "" {
		fmt.Println(text)
	}
	if len(result.StructuredContent) > 0 {
		log.Print("structured content:")
		fmt.Println(pretty(result.StructuredContent))
	}
	return nil
}

// pretty renders JSON for reading, falling back to the raw bytes.
func pretty(raw json.RawMessage) string {
	var buf any
	if err := json.Unmarshal(raw, &buf); err != nil {
		return string(raw)
	}
	out, err := json.MarshalIndent(buf, "", "  ")
	if err != nil {
		return string(raw)
	}
	return string(out)
}

// firstLine trims a description to its first line.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
