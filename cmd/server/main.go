package main

import (
	"net/http"

	"github.com/SergeiMurashev/bitrix-passport-exporter/internal/config"
	httpapi "github.com/SergeiMurashev/bitrix-passport-exporter/internal/http"
	log "github.com/sirupsen/logrus"
)

func main() {
	log.SetFormatter(&log.TextFormatter{
		FullTimestamp: true,
	})
	log.SetLevel(log.InfoLevel)

	cfg := config.Load()
	mux := http.NewServeMux()
	h := httpapi.New(cfg)
	h.Register(mux)

	log.WithField("addr", cfg.Addr).Info("bitrix-passport-exporter started")
	if err := http.ListenAndServe(cfg.Addr, mux); err != nil {
		log.WithError(err).Fatal("http server stopped")
	}
}
