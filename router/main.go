package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"time"
)

func main() {
	addr := flag.String("addr", ":8080", "Listen address")
	flag.Parse()

	// Default config — in production this comes from a config file or registry
	providers := []ProviderConfig{
		{
			ID:            "reference-context",
			Endpoint:      "http://localhost:8081",
			ContextMatch:  true,
			IdentityMatch: false,
			WireFormats:   []string{"json"},
			Timeout:       30 * time.Millisecond,
		},
		{
			ID:            "reference-identity",
			Endpoint:      "http://localhost:8082",
			ContextMatch:  false,
			IdentityMatch: true,
			WireFormats:   []string{"json"},
			Timeout:       30 * time.Millisecond,
		},
	}

	registry := NewRegistry("", "") // No remote sync for default config
	router := NewRouter(providers, registry, nil) // No signing for default config

	mux := http.NewServeMux()
	mux.HandleFunc("POST /tmp/context", router.HandleContextMatch)
	mux.HandleFunc("POST /tmp/identity", router.HandleIdentityMatch)
	mux.HandleFunc("GET /registry/snapshot", registry.HandleSnapshot)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "ok")
	})

	log.Printf("TMP Router listening on %s with %d providers", *addr, len(providers))
	log.Fatal(http.ListenAndServe(*addr, mux))
}
