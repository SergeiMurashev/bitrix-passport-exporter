package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/SergeiMurashev/bitrix-passport-exporter/internal/models"
)

type Config struct {
	Addr                          string
	BitrixAppClientID             string
	BitrixAppClientSecret         string
	PortalApps                    map[string]PortalAppConfig
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

type PortalAppConfig struct {
	Alias        string
	Domain       string
	ClientID     string
	ClientSecret string
}

func Load() Config {
	loadDotEnvIfPresent()
	portalApps := loadPortalAppsFromEnv()

	return Config{
		Addr:                          env("ADDR", ":25504"),
		BitrixAppClientID:             env("BITRIX_APP_CLIENT_ID", ""),
		BitrixAppClientSecret:         env("BITRIX_APP_CLIENT_SECRET", ""),
		PortalApps:                    portalApps,
		TaskWorkers:                   envInt("TASK_WORKERS", 10),
		TaskStrategy:                  env("TASK_STRATEGY", models.StrategyPerDeal),
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
	if strategy != models.StrategyBulk && strategy != models.StrategyPerDeal {
		return fmt.Errorf("TASK_STRATEGY must be one of: bulk, per_deal (got %q)", c.TaskStrategy)
	}
	if len(c.PortalApps) == 0 {
		if strings.TrimSpace(c.BitrixAppClientID) == "" {
			return errors.New("BITRIX_APP_CLIENT_ID is empty")
		}
		if strings.TrimSpace(c.BitrixAppClientSecret) == "" {
			return errors.New("BITRIX_APP_CLIENT_SECRET is empty")
		}
	}
	if c.RateLimitExportPerMinute < 0 {
		return errors.New("RATE_LIMIT_EXPORT_PER_MINUTE must be >= 0")
	}
	return nil
}

func (c Config) ResolvePortalApp(domain string) (clientID, clientSecret string, ok bool) {
	normalizedDomain := normalizeDomain(domain)
	if normalizedDomain != "" {
		if app, exists := c.PortalApps[normalizedDomain]; exists {
			return app.ClientID, app.ClientSecret, true
		}
	}

	clientID = strings.TrimSpace(c.BitrixAppClientID)
	clientSecret = strings.TrimSpace(c.BitrixAppClientSecret)
	if clientID == "" || clientSecret == "" {
		return "", "", false
	}
	return clientID, clientSecret, true
}

func loadPortalAppsFromEnv() map[string]PortalAppConfig {
	aliasesRaw := strings.TrimSpace(env("PORTALS", ""))
	if aliasesRaw == "" {
		return map[string]PortalAppConfig{}
	}

	out := make(map[string]PortalAppConfig)
	aliases := strings.Split(aliasesRaw, ",")
	for _, aliasRaw := range aliases {
		alias := strings.TrimSpace(aliasRaw)
		if alias == "" {
			continue
		}
		key := strings.ToUpper(alias)
		domain := normalizeDomain(env("B24_"+key+"_DOMAIN", ""))
		clientID := strings.TrimSpace(env("B24_"+key+"_CLIENT_ID", ""))
		clientSecret := strings.TrimSpace(env("B24_"+key+"_CLIENT_SECRET", ""))
		if domain == "" || clientID == "" || clientSecret == "" {
			fmt.Printf("skip portal %q: incomplete B24_%s_* config\n", alias, key)
			continue
		}
		out[domain] = PortalAppConfig{
			Alias:        alias,
			Domain:       domain,
			ClientID:     clientID,
			ClientSecret: clientSecret,
		}
	}
	return out
}

func normalizeDomain(raw string) string {
	value := strings.TrimSpace(strings.ToLower(raw))
	if value == "" {
		return ""
	}
	value = strings.TrimPrefix(value, "https://")
	value = strings.TrimPrefix(value, "http://")
	value = strings.TrimSuffix(value, "/")
	return value
}

func loadDotEnvIfPresent() {
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
