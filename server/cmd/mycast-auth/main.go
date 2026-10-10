// Command mycast-auth authorises mycast with Netatmo once, from a machine with
// a browser, and stores the resulting token where the deployed functions can
// use it.
//
//	go run ./cmd/mycast-auth -table mycast-prod        # token into DynamoDB
//	go run ./cmd/mycast-auth -file ~/.mycast/tokens.json
//
// NETATMO_CLIENT_ID and NETATMO_CLIENT_SECRET are read from the environment or
// ./.env, like the server. AWS credentials come from the usual chain.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/rehabaam/mycast/config"
	"github.com/rehabaam/mycast/dynamo"
	"github.com/rehabaam/mycast/netatmo"
)

func main() {
	table := flag.String("table", os.Getenv("MYCAST_TABLE"), "DynamoDB table to store the token in")
	file := flag.String("file", "", "write the token to this file instead of DynamoDB")
	flag.Parse()

	if (*table == "") == (*file == "") {
		log.Fatal("give exactly one of -table (or MYCAST_TABLE) and -file")
	}
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("configuration: %v", err)
	}
	if cfg.ClientID == "" || cfg.ClientSecret == "" {
		log.Fatal("NETATMO_CLIENT_ID and NETATMO_CLIENT_SECRET must be set (environment or .env)")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var store netatmo.TokenStore
	where := *file
	if *table != "" {
		st, err := dynamo.Open(ctx, *table)
		if err != nil {
			log.Fatalf("open DynamoDB: %v", err)
		}
		store, where = st.TokenStore(), "DynamoDB table "+*table
	} else {
		store = netatmo.NewFileTokenStore(*file)
	}

	auth := netatmo.NewAuthenticatorWithStore(cfg.ClientID, cfg.ClientSecret, cfg.RedirectURL, store)
	if _, err := auth.Authorize(ctx); err != nil {
		log.Fatalf("authorisation failed: %v", err)
	}
	fmt.Printf("\nDone: the Netatmo token is stored in %s.\n", where)
}
