package anna

import (
	"strings"

	"github.com/billmal071/bookdl/internal/config"
)

// NormalizeHost reduces a configured base URL to a bare host. It accepts bare
// hosts, scheme-qualified origins and trailing slashes, so every consumer can
// build URLs as "https://<host>/..." without repeating the cleanup. The config
// default ("annas-archive.gl") and a pasted "https://annas-archive.gl/" both
// work.
func NormalizeHost(baseURL string) string {
	host := strings.TrimSpace(baseURL)
	host = strings.TrimPrefix(host, "https://")
	host = strings.TrimPrefix(host, "http://")
	host = strings.TrimSuffix(host, "/")
	if host == "" {
		return "annas-archive.gl"
	}
	return host
}

// NewClient creates a new Anna's Archive client
// It uses the API client if an API key is configured, otherwise falls back to scraping
func NewClient() Client {
	cfg := config.Get()

	if cfg.Anna.APIKey != "" {
		return NewAPIClient(cfg.Anna.APIKey, cfg.Anna.BaseURL)
	}

	return NewScraperClient(cfg.Anna.BaseURL)
}

// GetBaseURL returns the configured base URL
func GetBaseURL() string {
	cfg := config.Get()
	if cfg.Anna.BaseURL != "" {
		return cfg.Anna.BaseURL
	}
	return "annas-archive.gl"
}
