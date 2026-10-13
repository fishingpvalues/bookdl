package search

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/billmal071/bookdl/internal/anna"
	"github.com/billmal071/bookdl/internal/liber3"
	"github.com/billmal071/bookdl/internal/zlibrary"
)

// Option defines which sources to search
type Option string

const (
	OptionAll      Option = "all"
	OptionAnna     Option = "anna"
	OptionZLibrary Option = "zlibrary"
	OptionLiber3   Option = "liber3"
)

// SourceStatus describes how one source fared during a multi-source search.
type SourceStatus struct {
	Source  string `json:"source"`
	State   string `json:"state"` // ok, empty, degraded
	Results int    `json:"results"`
	Err     string `json:"error,omitempty"`
}

// Source states reported in SourceStatus.State.
const (
	StatusOK       = "ok"       // returned at least one result
	StatusEmpty    = "empty"    // reachable, but matched nothing
	StatusDegraded = "degraded" // unreachable/blocked; other sources still count
	StatusPending  = "pending"  // not waited for; another source already answered
)

// ErrAllSourcesFailed is returned when no configured source produced a result.
var ErrAllSourcesFailed = errors.New("all search sources failed")

// Outcome is the result of a search plus per-source bookkeeping. Warnings are
// non-fatal notes about sources that degraded; Results may still be complete.
type Outcome struct {
	Books    []*anna.Book
	Statuses []SourceStatus
}

// Warnings renders the degraded and abandoned sources as human-readable notes.
func (o *Outcome) Warnings() []string {
	if o == nil {
		return nil
	}
	var out []string
	for _, st := range o.Statuses {
		if st.State != StatusDegraded && st.State != StatusPending {
			continue
		}
		msg := fmt.Sprintf("source %q unavailable", st.Source)
		if st.Err != "" {
			msg += ": " + st.Err
		}
		out = append(out, msg)
	}
	return out
}

// Searcher orchestrates searches across multiple sources
type Searcher struct {
	opt Option
}

// NewSearcher creates a new multi-source searcher
func NewSearcher(opt Option) *Searcher {
	return &Searcher{opt: opt}
}

// Search searches across configured sources and returns aggregated results.
//
// A single dead or blocked source never aborts the search: results from the
// surviving sources are returned. The error is only non-nil when nothing at all
// could be produced. Use SearchWithStatus when per-source diagnostics are
// wanted (the CLI does, to print warnings).
func (s *Searcher) Search(ctx context.Context, query string, limit int) ([]*anna.Book, error) {
	outcome := s.SearchWithStatus(ctx, query, limit)
	if len(outcome.Books) == 0 && outcome.Failed() {
		return nil, outcome.Err()
	}
	return outcome.Books, nil
}

// Failed reports whether every configured source failed.
func (o *Outcome) Failed() bool {
	for _, st := range o.Statuses {
		if st.State != StatusDegraded {
			return false
		}
	}
	return len(o.Statuses) > 0
}

// Degraded reports whether any source failed or was abandoned.
func (o *Outcome) Degraded() bool {
	if o == nil {
		return false
	}
	for _, st := range o.Statuses {
		if st.State == StatusDegraded || st.State == StatusPending {
			return true
		}
	}
	return false
}

// Err summarises why the search produced nothing.
func (o *Outcome) Err() error {
	if len(o.Statuses) == 0 {
		return ErrAllSourcesFailed
	}
	first := ""
	for _, st := range o.Statuses {
		if st.Err != "" {
			first = st.Err
			break
		}
	}
	if first == "" {
		return ErrAllSourcesFailed
	}
	return fmt.Errorf("%w: %s", ErrAllSourcesFailed, first)
}

// SearchWithStatus searches the configured sources and reports, per source,
// whether it produced results, matched nothing, or degraded.
func (s *Searcher) SearchWithStatus(ctx context.Context, query string, limit int) *Outcome {
	switch s.opt {
	case OptionAnna:
		return single(ctx, "anna", AnnaSourceBudget, func() ([]*anna.Book, error) { return s.searchAnna(ctx, query, limit) })
	case OptionZLibrary:
		return single(ctx, "zlibrary", ZLibrarySourceBudget, func() ([]*anna.Book, error) { return s.searchZLibrary(ctx, query, limit) })
	case OptionLiber3:
		return single(ctx, "liber3", Liber3SourceBudget, func() ([]*anna.Book, error) { return s.searchLiber3(ctx, query, limit) })
	default:
		return s.searchAll(ctx, query, limit)
	}
}

// single wraps one source in an Outcome, under the same per-source budget the
// multi-source path uses: an explicitly requested source gets no more time than
// it would get inside "--source all".
func single(ctx context.Context, name string, budget time.Duration, fn func() ([]*anna.Book, error)) *Outcome {
	srcCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	books, err := fn()
	if srcCtx.Err() == context.DeadlineExceeded && len(books) == 0 {
		err = fmt.Errorf("timed out after %s", budget)
	}
	return &Outcome{Books: books, Statuses: []SourceStatus{statusFor(name, books, err)}}
}

// statusFor turns a per-source result into a SourceStatus.
//
// "no results" is a normal outcome, not a degraded source: a healthy source that
// matched nothing must not be reported as unavailable.
func statusFor(name string, books []*anna.Book, err error) SourceStatus {
	st := SourceStatus{Source: name, Results: len(books)}
	switch {
	case err != nil && isNoResults(err):
		st.State = StatusEmpty
		st.Err = ""
	case err != nil:
		st.State = StatusDegraded
		st.Err = err.Error()
	case len(books) == 0:
		st.State = StatusEmpty
	default:
		st.State = StatusOK
	}
	return st
}

// isNoResults recognises the per-source "matched nothing" sentinel. The sources
// wrap it inconsistently, so both errors.Is and a message check are used.
func isNoResults(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, anna.ErrNoResults) || errors.Is(err, zlibrary.ErrNoResults) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no results found") || strings.Contains(msg, "no results")
}

// searchZLibrary searches only Z-Library
func (s *Searcher) searchZLibrary(ctx context.Context, query string, limit int) ([]*anna.Book, error) {
	client := zlibrary.NewClient()
	books, err := client.Search(ctx, query, limit)
	if err != nil {
		return nil, err
	}

	// Convert zlibrary.Book to anna.Book and add source annotation
	var annaBooks []*anna.Book
	for _, book := range books {
		annaBook := &anna.Book{
			MD5Hash:     book.MD5Hash,
			Title:       book.Title,
			Authors:     book.Authors,
			Publisher:   book.Publisher,
			Year:        book.Year,
			Language:    book.Language,
			Format:      book.Format,
			Size:        book.Size,
			SizeBytes:   book.SizeBytes,
			PageURL:     book.PageURL,
			DownloadURL: book.DownloadURL,
			Source:      "zlibrary",
		}
		annaBooks = append(annaBooks, annaBook)
	}

	return annaBooks, nil
}

// searchLiber3 searches only Liber3
func (s *Searcher) searchLiber3(ctx context.Context, query string, limit int) ([]*anna.Book, error) {
	client := liber3.NewClient()
	books, err := client.Search(ctx, query, limit)
	if err != nil {
		return nil, err
	}

	// Convert liber3.Book to anna.Book and add source annotation
	var annaBooks []*anna.Book
	for _, book := range books {
		annaBook := &anna.Book{
			MD5Hash:     book.MD5Hash,
			Title:       book.Title,
			Authors:     book.Authors,
			Publisher:   book.Publisher,
			Year:        book.Year,
			Language:    book.Language,
			Format:      book.Format,
			Size:        book.Size,
			SizeBytes:   book.SizeBytes,
			PageURL:     book.PageURL,
			DownloadURL: book.DownloadURL,
			Source:      "liber3",
		}
		annaBooks = append(annaBooks, annaBook)
	}

	return annaBooks, nil
}

// searchAnna searches only Anna's Archive
func (s *Searcher) searchAnna(ctx context.Context, query string, limit int) ([]*anna.Book, error) {
	client := anna.NewClient()
	books, err := client.Search(ctx, query, limit)
	if err != nil {
		return nil, err
	}

	// Add source annotation
	for _, book := range books {
		book.Source = "anna"
	}

	return books, nil
}

// Per-source budgets. Measured in the bookdl container on 2026-10-13: the anna
// path needs ~15-60s (browser start plus the DDoS-Guard wait), zlibrary's
// browser scrape burns ~55s and still returns nothing, and liber3 fails in
// ~1s. A hanging source must not delay or suppress rows another source already
// produced, so every source gets its own bound and the search returns as soon
// as one of them has something to show.
const (
	// Two render attempts (the DDoS-Guard challenge is intermittent) plus browser
	// start-up have to fit inside this budget.
	AnnaSourceBudget     = 150 * time.Second
	ZLibrarySourceBudget = 12 * time.Second
	Liber3SourceBudget   = 15 * time.Second
	DefaultSourceBudget  = 20 * time.Second
)

// searchAll queries every source concurrently, each under its own budget.
//
// It returns as soon as a source produced rows (the remaining sources are
// reported as still running), or when every source has settled. This is what
// keeps a dead or hanging backend from turning "--source all" into a command
// that prints nothing for minutes.
func (s *Searcher) searchAll(ctx context.Context, query string, limit int) *Outcome {
	type result struct {
		name  string
		books []*anna.Book
		err   error
	}

	sources := []struct {
		name   string
		budget time.Duration
		fn     func(context.Context, string, int) ([]*anna.Book, error)
	}{
		{"anna", AnnaSourceBudget, s.searchAnna},
		{"zlibrary", ZLibrarySourceBudget, s.searchZLibrary},
		{"liber3", Liber3SourceBudget, s.searchLiber3},
	}

	results := make(chan result, len(sources))

	// Every browser-backed source starts its own Chromium. Running them at the
	// same time starves the one source that actually works: measured
	// 2026-10-13, the anna search takes ~18s alone but ~40s while zlibrary's
	// Chromium is up. The first-listed source (the one expected to answer) takes
	// the browser, and a slow source only gets it once rows exist or the budget
	// is spent.
	browserGate := make(chan struct{}, 1)
	for i, src := range sources {
		if i == 0 {
			browserGate <- struct{}{}
		}
		go func(name string, budget time.Duration, fn func(context.Context, string, int) ([]*anna.Book, error), needGate bool) {
			srcCtx, cancel := context.WithTimeout(ctx, budget)
			defer cancel()
			if needGate {
				select {
				case <-browserGate:
				case <-srcCtx.Done():
					results <- result{name: name, err: fmt.Errorf("timed out after %s waiting for the browser", budget)}
					return
				}
			}
			books, err := fn(srcCtx, query, limit)
			if needGate {
				browserGate <- struct{}{}
			}
			if srcCtx.Err() == context.DeadlineExceeded && len(books) == 0 {
				err = fmt.Errorf("timed out after %s", budget)
			}
			results <- result{name: name, books: books, err: err}
		}(src.name, src.budget, src.fn, i > 0)
	}

	outcome := &Outcome{}
	for done := 0; done < len(sources); {
		res := <-results
		done++
		outcome.Books = append(outcome.Books, res.books...)
		outcome.Statuses = append(outcome.Statuses, statusFor(res.name, res.books, res.err))

		// Something to show: return now rather than waiting on a slow source.
		if len(outcome.Books) > 0 {
			for _, src := range sources {
				if !outcome.hasStatus(src.name) {
					outcome.Statuses = append(outcome.Statuses, SourceStatus{
						Source: src.name,
						State:  StatusPending,
						Err:    "still running when results were returned; not waited for",
					})
				}
			}
			break
		}
	}

	sort.SliceStable(outcome.Statuses, func(i, j int) bool {
		return outcome.Statuses[i].Source < outcome.Statuses[j].Source
	})
	return outcome
}

// hasStatus reports whether a source already has a status entry.
func (o *Outcome) hasStatus(name string) bool {
	for _, st := range o.Statuses {
		if st.Source == name {
			return true
		}
	}
	return false
}

// SearchPage searches across sources with pagination support
func (s *Searcher) SearchPage(ctx context.Context, query string, limit int, page int) ([]*anna.Book, error) {
	// For now, delegate to Search (pagination not essential for multi-source support)
	return s.Search(ctx, query, limit)
}

// GetDownloadInfo retrieves download information for a book from the appropriate source
func (s *Searcher) GetDownloadInfo(ctx context.Context, md5Hash string, source string) (*anna.DownloadInfo, error) {
	// TODO: Route to correct source client
	client := anna.NewClient()
	return client.GetDownloadInfo(ctx, md5Hash)
}
