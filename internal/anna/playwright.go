package anna

import (
	"bytes"
	"context"
	"fmt"
	"html"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// This file drives Anna's Archive through Playwright instead of chromedp.
//
// Why: the mirror answers non-browser HTTP clients with a DDoS-Guard
// interstitial (HTTP 302 -> 403, Server: ddos-guard). Passing it requires a
// real browser that stays on the page while the challenge navigates itself.
// Measured inside the bookdl container on 2026-10-13:
//
//   - chromedp (v0.9.5) never resolved the interstitial, with or without
//     --disable-blink-features=AutomationControlled, with the real chromium
//     binary instead of the wrapper, and with a fresh or reused profile.
//   - Playwright, launching the very same chromium binary with the same flags
//     and the same user agent, resolved it in ~13s.
//
// The difference is Playwright's launch/CND transport, not the browser binary,
// so the browser step is delegated to Playwright and the HTML is parsed in Go
// with the selectors verified against the live page (2026-10-13).

// recordSplit separates result rows in the rendered search page. Verified live:
// 50 records per page.
const recordSplit = `<div class="flex  pt-3 pb-3 border-b`

// stealthUserAgent hides the HeadlessChrome token and the true browser version
// (Chromium 152 in the current image). Verified: the challenge is not solved
// with the stock headless UA.
const stealthUserAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/148.0.0.0 Safari/537.36"

const playwrightTimeout = 120 * time.Second

var (
	reScript       = regexp.MustCompile(`(?is)<script.*?</script>`)
	reTags         = regexp.MustCompile(`<[^>]+>`)
	reMD5          = regexp.MustCompile(`/md5/([0-9a-fA-F]{32})`)
	reTitle        = regexp.MustCompile(`(?s)<a href="/md5/[0-9a-fA-F]{32}"[^>]*font-semibold text-lg[^>]*>(.*?)</a>`)
	reAuthor       = regexp.MustCompile(`(?s)icon-\[mdi--user-edit\][^>]*></span>(.*?)</a>`)
	reMeta         = regexp.MustCompile(`(?s)font-semibold text-sm leading-\[1\.2\] mt-2">(.*?)<`)
	reSizePart     = regexp.MustCompile(`^[\d.]+\s*[KMGT]?B$`)
	reYearPart     = regexp.MustCompile(`^\d{4}$`)
	reFormatPart   = regexp.MustCompile(`^[A-Z0-9]{2,5}$`)
	reLanguageName = regexp.MustCompile(`^[A-Za-z ()-]{1,40}$`)
)

// PlaywrightClient runs the bundled Playwright search script and parses its
// output. It is a BrowserClient-compatible alternative to the chromedp path.
type PlaywrightClient struct {
	baseURL  string
	script   string
	python   string
	fallback *BrowserClient
}

// NewPlaywrightClient creates a Playwright-backed client. An empty script path
// disables it.
func NewPlaywrightClient(baseURL, script, python string) *PlaywrightClient {
	if python == "" {
		python = "python3"
	}
	return &PlaywrightClient{
		baseURL:  baseURL,
		script:   script,
		python:   python,
		fallback: NewBrowserClient(baseURL),
	}
}

// defaultHelperPaths are searched when no helper is configured, so the anna
// path works in the bookdl image (and against a bookdl-web style layout)
// without extra configuration.
var defaultHelperPaths = []string{
	"/usr/local/bin/anna-search.py",
	"/app/stealth-search.py",
	"/usr/local/bin/stealth-search.py",
}

// helperPath resolves the Playwright helper script actually to use.
func (c *PlaywrightClient) helperPath() string {
	if c == nil {
		return ""
	}
	if c.script != "" {
		if _, err := os.Stat(c.script); err == nil {
			return c.script
		}
	}
	for _, candidate := range defaultHelperPaths {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return ""
}

// Available reports whether a Playwright helper and interpreter are present.
func (c *PlaywrightClient) Available() bool {
	if c == nil {
		return false
	}
	if c.helperPath() == "" {
		return false
	}
	_, err := exec.LookPath(c.python)
	return err == nil
}

// Search searches using the Playwright helper.
func (c *PlaywrightClient) Search(ctx context.Context, query string, limit int) ([]*Book, error) {
	return c.SearchPage(ctx, query, limit, 1)
}

// SearchPage renders the search page through the Playwright helper and parses
// the returned HTML. The helper only renders page 1; later pages fall through to
// the chromedp client, which understands the page parameter.
func (c *PlaywrightClient) SearchPage(ctx context.Context, query string, limit int, page int) ([]*Book, error) {
	if !c.Available() {
		return nil, fmt.Errorf("playwright helper not available")
	}
	if page > 1 {
		return c.fallback.SearchPage(ctx, query, limit, page)
	}

	runCtx, cancel := context.WithTimeout(ctx, playwrightTimeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, c.python, c.helperPath(), query, "https://"+NormalizeHost(c.baseURL))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		if runCtx.Err() != nil {
			msg = "timed out waiting for the DDoS-Guard challenge"
		}
		return nil, fmt.Errorf("playwright search failed: %s", firstLine(msg))
	}

	return parseSearchResultsHTML(string(out), limit, c.baseURL)
}

// GetDownloadInfo delegates to the chromedp client, which handles the md5 page.
func (c *PlaywrightClient) GetDownloadInfo(ctx context.Context, md5Hash string) (*DownloadInfo, error) {
	return c.fallback.GetDownloadInfo(ctx, md5Hash)
}

// ResolveDownloadURL delegates to the chromedp client.
func (c *PlaywrightClient) ResolveDownloadURL(ctx context.Context, slowDownloadURL string) (string, error) {
	return c.fallback.ResolveDownloadURL(ctx, slowDownloadURL)
}

// parseSearchResultsHTML parses a rendered Anna's Archive search page.
//
// The selectors are the ones proven against the live page by the bookdl-web
// helper (stealth-search.py); the same shapes were re-verified against a live
// capture on 2026-10-13: 50 record blocks, one font-semibold text-lg title
// anchor and one mdi--user-edit author span each.
func parseSearchResultsHTML(htmlContent string, limit int, baseURL string) ([]*Book, error) {
	// The parsed fragments are inner HTML of the title/author anchors, so
	// embedded markup (for example the "Save" widget inside the metadata line)
	// has to go before unescaping.
	text := func(fragment string) string {
		fragment = reScript.ReplaceAllString(fragment, " ")
		fragment = reTags.ReplaceAllString(fragment, "")
		fragment = html.UnescapeString(fragment)
		return strings.TrimSpace(strings.Join(strings.Fields(fragment), " "))
	}

	blocks := strings.Split(htmlContent, recordSplit)
	if len(blocks) < 2 {
		if strings.Contains(htmlContent, "No files found") {
			return nil, ErrNoResults
		}
		return nil, fmt.Errorf("no result rows in page (DDoS-Guard interstitial or layout change)")
	}

	var books []*Book
	seen := make(map[string]bool)
	for _, block := range blocks[1:] {
		if len(books) >= limit {
			break
		}

		md5Match := reMD5.FindStringSubmatch(block)
		if len(md5Match) < 2 {
			continue
		}
		md5 := strings.ToLower(md5Match[1])
		if seen[md5] {
			continue
		}
		seen[md5] = true

		book := &Book{
			MD5Hash: md5,
			PageURL: fmt.Sprintf("https://%s/md5/%s", baseURL, md5),
			Source:  "anna",
		}

		if m := reTitle.FindStringSubmatch(block); len(m) > 1 {
			book.Title = text(m[1])
		}
		if m := reAuthor.FindStringSubmatch(block); len(m) > 1 {
			book.Authors = text(m[1])
		}

		// Metadata line: "English [en] · EPUB · 1.1MB · 2019 · Book (fiction)".
		// Fields can be missing, so classify each part instead of assuming order.
		if m := reMeta.FindStringSubmatch(block); len(m) > 1 {
			for _, part := range strings.Split(text(m[1]), "·") {
				part = strings.TrimSpace(part)
				switch {
				case part == "":
				case reSizePart.MatchString(part):
					book.Size = part
				case reYearPart.MatchString(part):
					book.Year = part
				case reFormatPart.MatchString(part):
					book.Format = strings.ToUpper(part)
				case strings.Contains(part, "[") && book.Language == "":
					lang := strings.TrimSpace(strings.SplitN(part, "[", 2)[0])
					if reLanguageName.MatchString(lang) {
						book.Language = lang
					}
				}
			}
		}

		if book.Title == "" {
			continue
		}
		book.SizeBytes = parseSizeBytes(book.Size)
		books = append(books, book)
	}

	if len(books) == 0 {
		return nil, ErrNoResults
	}
	return books, nil
}

// parseSizeBytes converts "1.1MB" to bytes; 0 when unknown.
func parseSizeBytes(s string) int64 {
	s = strings.TrimSpace(strings.ToUpper(s))
	if s == "" {
		return 0
	}
	mult := int64(1)
	switch {
	case strings.HasSuffix(s, "KB"):
		mult, s = 1024, strings.TrimSuffix(s, "KB")
	case strings.HasSuffix(s, "MB"):
		mult, s = 1024*1024, strings.TrimSuffix(s, "MB")
	case strings.HasSuffix(s, "GB"):
		mult, s = 1024*1024*1024, strings.TrimSuffix(s, "GB")
	case strings.HasSuffix(s, "TB"):
		mult, s = 1024*1024*1024*1024, strings.TrimSuffix(s, "TB")
	case strings.HasSuffix(s, "B"):
		mult, s = 1, strings.TrimSuffix(s, "B")
	default:
		return 0
	}
	value, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || value <= 0 {
		return 0
	}
	return int64(value * float64(mult))
}

// searchURL builds the search URL for a page.
func searchURL(baseURL, query string, page int) string {
	u := fmt.Sprintf("https://%s/search?q=%s", NormalizeHost(baseURL), url.QueryEscape(query))
	if page > 1 {
		u = fmt.Sprintf("%s&page=%d", u, page)
	}
	return u
}

// firstLine keeps multi-line helper errors to a single readable line.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}
