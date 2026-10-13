package search

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

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
)

// ErrAllSourcesFailed is returned when no configured source produced a result.
var ErrAllSourcesFailed = errors.New("all search sources failed")

// Outcome is the result of a search plus per-source bookkeeping. Warnings are
// non-fatal notes about sources that degraded; Results may still be complete.
type Outcome struct {
	Books    []*anna.Book
	Statuses []SourceStatus
}

// Warnings renders the degraded sources as human-readable notes.
func (o *Outcome) Warnings() []string {
	if o == nil {
		return nil
	}
	var out []string
	for _, st := range o.Statuses {
		if st.State != StatusDegraded {
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

// Degraded reports whether any source failed.
func (o *Outcome) Degraded() bool {
	if o == nil {
		return false
	}
	for _, st := range o.Statuses {
		if st.State == StatusDegraded {
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
		return single(ctx, "anna", func() ([]*anna.Book, error) { return s.searchAnna(ctx, query, limit) })
	case OptionZLibrary:
		return single(ctx, "zlibrary", func() ([]*anna.Book, error) { return s.searchZLibrary(ctx, query, limit) })
	case OptionLiber3:
		return single(ctx, "liber3", func() ([]*anna.Book, error) { return s.searchLiber3(ctx, query, limit) })
	default:
		return s.searchAll(ctx, query, limit)
	}
}

// single wraps one source in an Outcome.
func single(_ context.Context, name string, fn func() ([]*anna.Book, error)) *Outcome {
	books, err := fn()
	return &Outcome{Books: books, Statuses: []SourceStatus{statusFor(name, books, err)}}
}

// statusFor turns a per-source result into a SourceStatus.
func statusFor(name string, books []*anna.Book, err error) SourceStatus {
	st := SourceStatus{Source: name, Results: len(books)}
	switch {
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

// searchAll queries every source concurrently. One dead, blocked or slow
// source must not abort the command: whatever the survivors returned is
// returned, and the failures are reported in Outcome.Statuses.
func (s *Searcher) searchAll(ctx context.Context, query string, limit int) *Outcome {
	type result struct {
		name  string
		books []*anna.Book
		err   error
	}

	sources := []struct {
		name string
		fn   func(context.Context, string, int) ([]*anna.Book, error)
	}{
		{"anna", s.searchAnna},
		{"zlibrary", s.searchZLibrary},
		{"liber3", s.searchLiber3},
	}

	results := make(chan result, len(sources))
	var wg sync.WaitGroup
	for _, src := range sources {
		wg.Add(1)
		go func(name string, fn func(context.Context, string, int) ([]*anna.Book, error)) {
			defer wg.Done()
			books, err := fn(ctx, query, limit)
			results <- result{name: name, books: books, err: err}
		}(src.name, src.fn)
	}
	wg.Wait()
	close(results)

	outcome := &Outcome{}
	for res := range results {
		outcome.Books = append(outcome.Books, res.books...)
		outcome.Statuses = append(outcome.Statuses, statusFor(res.name, res.books, res.err))
	}

	// Stable order regardless of goroutine scheduling.
	sort.SliceStable(outcome.Statuses, func(i, j int) bool {
		return outcome.Statuses[i].Source < outcome.Statuses[j].Source
	})

	return outcome
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
