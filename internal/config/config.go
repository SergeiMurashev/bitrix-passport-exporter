package config

import (
	"os"
	"strings"
)

type Config struct {
	Addr    string
	Webhook string
}

func Load() Config {
	return Config{
		Addr:    env("ADDR", ":8080"),
		Webhook: env("BITRIX_WEBHOOK_URL", ""),
	}
}

func env(key, fallback string) string {
	if v := os.Getenv(key); strings.TrimSpace(v) != "" {
		return v
	}
	return fallback
}
