package main

import (
	"log"
	"net/http"

	"github.com/SergeiMurashev/bitrix-passport-exporter/internal/config"
	httpapi "github.com/SergeiMurashev/bitrix-passport-exporter/internal/http"
)

func main() {
	cfg := config.Load()
	mux := http.NewServeMux()
	h := httpapi.New()
	h.Register(mux)

	log.Printf("bitrix-passport-exporter listen on %s", cfg.Addr)
	if err := http.ListenAndServe(cfg.Addr, mux); err != nil {
		log.Fatal(err)
	}
}
