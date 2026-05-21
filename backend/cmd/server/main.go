package main

import (
	"net/http"
	"path/filepath"
	"runtime"
	"strconv"

	"github.com/SergeiMurashev/bitrix-passport-exporter/internal/config"
	httpapi "github.com/SergeiMurashev/bitrix-passport-exporter/internal/http"
	log "github.com/sirupsen/logrus"
)

func main() {
	log.SetFormatter(&log.JSONFormatter{})
	log.SetLevel(log.InfoLevel)

	cfg := config.Load()
	mux := http.NewServeMux()
	h, err := httpapi.New(cfg)
	if err != nil {
		log.WithError(err).Fatal("failed to initialize handler")
	}
	defer h.Close()
	h.Register(mux)

	point, short := callerPoint(1)
	log.WithFields(log.Fields{
		"addr":        cfg.Addr,
		"point":       point,
		"short_point": short,
	}).Info("server started successfully")
	if err := http.ListenAndServe(cfg.Addr, mux); err != nil {
		log.WithError(err).Fatal("http server stopped")
	}
}

func callerPoint(skip int) (string, string) {
	_, file, line, ok := runtime.Caller(skip)
	if !ok {
		return "", ""
	}
	short := filepath.Base(filepath.Dir(file)) + "/" + filepath.Base(file) + ":" + strconv.Itoa(line)
	return file + ":" + strconv.Itoa(line), short
}
