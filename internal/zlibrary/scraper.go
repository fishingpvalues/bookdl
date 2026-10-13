package zlibrary

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/billmal071/bookdl/internal/config"
)

var (
	// ErrCloudflareBlocked indicates Cloudflare challenge detected
	ErrCloudflareBlocked = errors.New("cloudflare challenge detected")
	// ErrNoResults indicates no search results found
	ErrNoResults = errors.New("no results found")
)

// ScraperClient scrapes Z-Library website
type ScraperClient struct {
	baseURL string
	browser *BrowserClient
}

// NewScraperClient creates a new scraper client
func NewScraperClient(baseURL string) *ScraperClient {
	if baseURL == "" {
		cfg := config.Get()
		baseURL = cfg.ZLibrary.BaseURL
		if baseURL == "" {
			baseURL = "z-library.sk"
		}
	}
	return &ScraperClient{
		baseURL: baseURL,
		browser: NewBrowserClient(baseURL),
	}
}

// Search searches for books by scraping the website
func (c *ScraperClient) Search(ctx context.Context, query string, limit int) ([]*Book, error) {
	return c.SearchPage(ctx, query, limit, 1)
}

// SearchPage delegates directly to the browser client because Z-Library uses
// client-side rendering (Web Components with shadow DOM) for search results.
// The initial HTML returned by a plain HTTP request contains no book data.
//
// A plain HTTP preflight runs first: when the mirror answers with Cloudflare's
// challenge, launching a browser cannot help (measured 2026-10-13: the
// z-library.sk search page is answered by a Cloudflare interstitial even from a
// real browser, and the browser attempt burned ~37s before giving up). The
// preflight turns that into a fast, honest failure.
func (c *ScraperClient) SearchPage(ctx context.Context, query string, limit int, page int) ([]*Book, error) {
	if blocked, err := c.probeMirror(ctx, query); blocked {
		return nil, fmt.Errorf("%w: %s answers with a Cloudflare challenge", ErrCloudflareBlocked, c.baseURL)
	} else if err != nil {
		// Unreachable mirror: also not worth starting a browser for.
		return nil, fmt.Errorf("mirror %s unreachable: %w", c.baseURL, err)
	}
	return c.browser.SearchPage(ctx, query, limit, page)
}

// probeMirror performs one cheap HTTP request to decide whether starting a
// browser is worthwhile. It reports blocked=true when a bot challenge is in the
// response, and an error when the mirror could not be reached at all.
func (c *ScraperClient) probeMirror(ctx context.Context, query string) (bool, error) {
	cfg := config.Get()
	reqCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	probeURL := fmt.Sprintf("https://%s/s/%s", c.baseURL, url.PathEscape(query))
	req, err := http.NewRequestWithContext(reqCtx, "GET", probeURL, nil)
	if err != nil {
		return false, err
	}
	ua := cfg.Network.UserAgent
	if ua == "" {
		ua = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
	}
	req.Header.Set("User-Agent", ua)

	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	lower := strings.ToLower(string(body))
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusServiceUnavailable ||
		strings.Contains(lower, "just a moment") || strings.Contains(lower, "cf-chl") ||
		strings.Contains(lower, "cf_chl") || strings.Contains(lower, "cloudflare") ||
		strings.Contains(lower, "challenge-platform") {
		return true, nil
	}
	return false, nil
}

// GetDownloadInfo retrieves download links for a book.
// Z-Library requires login to show download links on book detail pages and
// uses Web Components (z-bookcard) that need a real browser to render.
// We delegate directly to the browser client which extracts download URLs
// from search results without authentication.
func (c *ScraperClient) GetDownloadInfo(ctx context.Context, md5Hash string) (*DownloadInfo, error) {
	return c.browser.GetDownloadInfo(ctx, md5Hash)
}
