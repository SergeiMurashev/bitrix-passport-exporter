package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Config struct {
	Addr                          string
	BitrixAppClientID             string
	BitrixAppClientSecret         string
	TaskWorkers                   int
	TaskStrategy                  string
	DeleteLastExportAfterDownload bool
	HTTPReadTimeoutSeconds        int
	HTTPWriteTimeoutSeconds       int
	HTTPIdleTimeoutSeconds        int
	HTTPShutdownTimeoutSecs       int
	RateLimitExportPerMinute      int
	SupportLinkDealField          string
	SupportMeasureValueField      string
	APIAccessToken                string
}

func Load() Config {
	loadDotEnvIfPresent()

	return Config{
		Addr:                          env("ADDR", ":25504"),
		BitrixAppClientID:             env("BITRIX_APP_CLIENT_ID", ""),
		BitrixAppClientSecret:         env("BITRIX_APP_CLIENT_SECRET", ""),
		TaskWorkers:                   envInt("TASK_WORKERS", 10),
		TaskStrategy:                  env("TASK_STRATEGY", "per_deal"),
		DeleteLastExportAfterDownload: envBool("DELETE_LAST_EXPORT_AFTER_DOWNLOAD", false),
		HTTPReadTimeoutSeconds:        envInt("HTTP_READ_TIMEOUT_SECONDS", 20),
		HTTPWriteTimeoutSeconds:       envInt("HTTP_WRITE_TIMEOUT_SECONDS", 3600),
		HTTPIdleTimeoutSeconds:        envInt("HTTP_IDLE_TIMEOUT_SECONDS", 120),
		HTTPShutdownTimeoutSecs:       envInt("HTTP_SHUTDOWN_TIMEOUT_SECONDS", 20),
		RateLimitExportPerMinute:      envIntAllowZero("RATE_LIMIT_EXPORT_PER_MINUTE", 6),
		SupportLinkDealField:          env("SUPPORT_LINK_DEAL_FIELD", "UF_CRM_1770268007"),
		SupportMeasureValueField:      env("SUPPORT_MEASURE_VALUE_FIELD", "UF_CRM_1744702884242"),
		APIAccessToken:                env("API_ACCESS_TOKEN", ""),
	}
}

func (c Config) Validate() error {
	if strings.TrimSpace(c.Addr) == "" {
		return errors.New("ADDR is empty")
	}
	strategy := strings.ToLower(strings.TrimSpace(c.TaskStrategy))
	if strategy != "bulk" && strategy != "per_deal" {
		return fmt.Errorf("TASK_STRATEGY must be one of: bulk, per_deal (got %q)", c.TaskStrategy)
	}
	if strings.TrimSpace(c.BitrixAppClientID) == "" {
		return errors.New("BITRIX_APP_CLIENT_ID is empty")
	}
	if strings.TrimSpace(c.BitrixAppClientSecret) == "" {
		return errors.New("BITRIX_APP_CLIENT_SECRET is empty")
	}
	if c.RateLimitExportPerMinute < 0 {
		return errors.New("RATE_LIMIT_EXPORT_PER_MINUTE must be >= 0")
	}
	return nil
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

func envIntAllowZero(key string, fallback int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		fmt.Printf("invalid %s=%q; using default %d\n", key, v, fallback)
		return fallback
	}
	return n
}

func envBool(key string, fallback bool) bool {
	v := strings.TrimSpace(strings.ToLower(os.Getenv(key)))
	if v == "" {
		return fallback
	}
	switch v {
	case "1", "true", "yes", "y", "on":
		return true
	case "0", "false", "no", "n", "off":
		return false
	default:
		fmt.Printf("invalid %s=%q; using default %t\n", key, v, fallback)
		return fallback
	}
}
