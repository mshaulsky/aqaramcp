// Command login signs in to the Aqara Agent service the way the login page
// does and reports what it got, without printing the key unless asked. Run it
// before trusting a program to sign in by itself:
//
//	export AQARA_USERNAME=... AQARA_REGION=RU
//	export AQARA_PASSWORD_MD5=$(printf %s "$password" | md5sum | cut -d' ' -f1)
//
//	go run ./examples/login              # sign in; print the region and the key's length
//	go run ./examples/login -status      # then read every device with the fresh key
//	go run ./examples/login -show-key    # print the key, for pasting into a config
//
// With AQARA_MCP_KEY set to a key issued earlier, it also reports whether that
// key still works after the new sign-in — whether a sign-in revokes the
// previous key.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"

	"github.com/mshaulsky/aqaramcp"
)

func main() {
	log.SetFlags(log.Ltime)
	log.SetPrefix("login: ")

	if err := execute(); err != nil {
		log.Fatal(err)
	}
}

// execute parses the flags, signs in and runs the checks.
func execute() error {
	var (
		status  = flag.Bool("status", false, "read every device with the fresh key")
		showKey = flag.Bool("show-key", false, "print the key itself")
	)
	flag.Parse()

	creds := aqaramcp.Credentials{
		Username:    os.Getenv("AQARA_USERNAME"),
		PasswordMD5: os.Getenv("AQARA_PASSWORD_MD5"),
		Region:      os.Getenv("AQARA_REGION"),
	}
	if creds.Username == "" || creds.Region == "" || creds.PasswordMD5 == "" {
		return errors.New("set AQARA_USERNAME, AQARA_PASSWORD_MD5 and AQARA_REGION")
	}
	var opts []aqaramcp.Option
	if endpoint := os.Getenv("AQARA_MCP_URL"); endpoint != "" {
		opts = append(opts, aqaramcp.WithEndpoint(endpoint))
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	key, err := aqaramcp.Login(ctx, creds, opts...)
	if err != nil {
		return err
	}
	log.Printf("signed in: region=%s, key of %d characters", key.Region, len(key.APIKey))
	if *showKey {
		fmt.Println(key.APIKey)
	}
	if *status {
		if err := showStatus(ctx, key.APIKey, opts); err != nil {
			return fmt.Errorf("with the fresh key: %w", err)
		}
	}
	if earlier := os.Getenv("AQARA_MCP_KEY"); earlier != "" {
		checkEarlier(ctx, earlier, opts)
	}
	return nil
}

// showStatus reads every device with the key and prints a line per device.
func showStatus(ctx context.Context, apiKey string, opts []aqaramcp.Option) error {
	client, err := aqaramcp.New(apiKey, opts...)
	if err != nil {
		return err
	}
	statuses, err := client.Statuses(ctx, aqaramcp.StatusFilter{})
	if err != nil {
		return err
	}
	log.Printf("%d device(s) read with the fresh key:", len(statuses))
	for _, s := range statuses {
		fmt.Printf("  %-22s %-18s online=%t\n", s.DeviceName, s.Type, s.Status.Online())
	}
	return nil
}

// checkEarlier reports whether a key issued before this sign-in still works.
func checkEarlier(ctx context.Context, apiKey string, opts []aqaramcp.Option) {
	client, err := aqaramcp.New(apiKey, opts...)
	if err != nil {
		log.Printf("earlier key: %v", err)
		return
	}
	_, err = client.Tools(ctx)
	var httpErr *aqaramcp.HTTPError
	switch {
	case err == nil:
		log.Print("the earlier key still works: signing in does not revoke it")
	case errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusUnauthorized:
		log.Print("the earlier key is rejected now: signing in revoked it")
	default:
		log.Printf("the earlier key could not be checked: %v", err)
	}
}
