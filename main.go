package main

import (
	"context"
	_ "embed"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/fiatjaf/khatru"
	"github.com/fiatjaf/khatru/policies"
	"github.com/kelseyhightower/envconfig"
	"github.com/nbd-wtf/go-nostr/nip11"
	"github.com/oschwald/maxminddb-golang"
	"github.com/rs/zerolog"
	"github.com/sebest/xff"
	"golang.org/x/sync/errgroup"
)

//go:embed GeoLite2-Country.mmdb
var maxmindData []byte

var mm *maxminddb.Reader
var (
	s     Settings
	log   = zerolog.New(os.Stderr).Output(zerolog.ConsoleWriter{Out: os.Stdout}).With().Timestamp().Logger()
	relay = khatru.NewRelay()
)

type Settings struct {
	Port         string `envconfig:"PORT" default:"40404"`
	BaseDomain   string `envconfig:"BASE_DOMAIN" required:"true"`
	DatabasePath string `envconfig:"DATABASE_PATH" default:"./db"`
	RelayContact string `envconfig:"RELAY_CONTACT" required:"false"`
	RelayIcon    string `envconfig:"RELAY_ICON" required:"false"`
}

// List of blocked countries in ISO 3166-1 alpha-2 format separated by comma.
// Example: BR,AU
const blockedCountries = ""

func main() {
	mm, _ = maxminddb.FromBytes(maxmindData)
	if mm == nil {
		log.Fatal().Msg("failed to open maxmind db")
		return
	}

	err := envconfig.Process("", &s)
	if err != nil {
		log.Fatal().Err(err).Msg("couldn't process envconfig")
		return
	}

	// init relay, see https://github.com/nostr-protocol/nips/blob/master/11.md
	relay.Info.Name = "countries"
	relay.Info.Description = "serves notes according to your nationality"
	relay.Info.Contact = s.RelayContact
	relay.Info.Icon = s.RelayIcon
	relay.Info.Limitation = &nip11.RelayLimitationDocument{}

	relay.StoreEvent = append(relay.StoreEvent, storeEventForCountryDB)

	relay.QueryEvents = append(relay.QueryEvents, queryEventForCountryDB)

	relay.DeleteEvent = append(relay.DeleteEvent, deleteEventForCountryDB)

	relay.RejectEvent = append(relay.RejectEvent,
		policies.PreventLargeTags(100),
		policies.PreventTooManyIndexableTags(8, []int{3, 10002}, nil),
		policies.PreventTooManyIndexableTags(1000, nil, []int{3, 10002}),
		rejectEventForCountryDB,
	)

	relay.RejectFilter = append(relay.RejectFilter,
		policies.NoSearchQueries,
		rejectFilterForCountryDB,
	)

	// http routes
	relay.Router().HandleFunc("/", homePage)

	log.Info().Msg("running on http://0.0.0.0:" + s.Port)

	xffmw, _ := xff.Default()
	server := &http.Server{Addr: ":" + s.Port, Handler: xffmw.Handler(relay)}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	g, ctx := errgroup.WithContext(ctx)
	g.Go(server.ListenAndServe)
	g.Go(func() error {
		<-ctx.Done()
		return server.Shutdown(context.Background())
	})

	if err := g.Wait(); err != nil {
		log.Debug().Err(err).Msg("exit reason")
	}
}
