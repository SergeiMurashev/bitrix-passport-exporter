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
		Addr:    env("ADDR", ":25504"),
		Webhook: env("BITRIX_WEBHOOK_URL", "https://b24-xo2ccz.bitrix24.ru/rest/1/n195u3mbvu1jkonf/"),
	}
}

func env(key, fallback string) string {
	if v := os.Getenv(key); strings.TrimSpace(v) != "" {
		return v
	}
	return fallback
}
