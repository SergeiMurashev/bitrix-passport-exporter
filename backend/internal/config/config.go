package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Config struct {
	Addr                     string
	Webhook                  string
	TaskWorkers              int
	TaskStrategy             string
	SupportLinkDealField     string
	SupportMeasureValueField string
	APIAccessToken           string
}

func Load() Config {
	loadDotEnvIfPresent()

	return Config{
		Addr:                     env("ADDR", ":25504"),
		Webhook:                  env("BITRIX_WEBHOOK_URL", ""),
		TaskWorkers:              envInt("TASK_WORKERS", 10),
		TaskStrategy:             env("TASK_STRATEGY", "per_deal"),
		SupportLinkDealField:     env("SUPPORT_LINK_DEAL_FIELD", "UF_CRM_1770268007"),
		SupportMeasureValueField: env("SUPPORT_MEASURE_VALUE_FIELD", "UF_CRM_1744702884242"),
		APIAccessToken:           env("API_ACCESS_TOKEN", ""),
	}
}

func loadDotEnvIfPresent() {
	// Поддержка общих каталогов запуска:
	// - корень проекта: ".env"
	// - внутренний каталог: "../.env"
	// - вложенные пути (резервный вариант): "../../.env"
	candidates := []string{".env", "../.env", "../../.env"}
	for _, path := range candidates {
		if !loadDotEnvFile(path) {
			continue
		}
		return
	}
}

func loadDotEnvFile(path string) bool {
	if _, err := os.Stat(path); err != nil {
		return false
	}

	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return false
	}
	defer f.Close()

	s := bufio.NewScanner(f)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		kv := strings.SplitN(line, "=", 2)
		if len(kv) != 2 {
			continue
		}
		k := strings.TrimSpace(kv[0])
		v := strings.TrimSpace(kv[1])
		if k == "" || v == "" {
			continue
		}
		v = strings.Trim(v, `"`)
		if strings.TrimSpace(os.Getenv(k)) == "" {
			_ = os.Setenv(k, v)
		}
	}

	return true
}

func env(key, fallback string) string {
	if v := os.Getenv(key); strings.TrimSpace(v) != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		fmt.Printf("invalid %s=%q; using default %d\n", key, v, fallback)
		return fallback
	}
	return n
}
