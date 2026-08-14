package main

import (
	"log"
	"net/http"

	"kimi-responses-adapter/internal/adapter"
)

func main() {
	cfg := adapter.LoadConfig()
	srv := adapter.NewServer(cfg)

	log.Printf("kimi-responses-adapter listening on %s", cfg.ListenAddr)
	log.Printf("upstream: %s (client credentials are forwarded; no keys held locally)", cfg.KimiBaseURL)
	if err := http.ListenAndServe(cfg.ListenAddr, srv.Handler()); err != nil {
		log.Fatal(err)
	}
}
