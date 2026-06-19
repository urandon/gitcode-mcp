package gitcode

import (
	"net/http"
	"net/url"
	"strconv"
)

type PaginationStrategy interface {
	Apply(url.Values, PageState)
	Next(http.Header, int) (PageState, bool)
}

type PageState struct {
	Page    int
	PerPage int
	Cursor  string
}

type PagePagination struct {
	Page    int
	PerPage int
}

func (p PagePagination) Apply(values url.Values, state PageState) {
	page := firstPositive(state.Page, p.Page, 1)
	perPage := firstPositive(state.PerPage, p.PerPage, 0)
	values.Set("page", strconv.Itoa(page))
	if perPage > 0 {
		values.Set("per_page", strconv.Itoa(perPage))
	}
}

func (p PagePagination) Next(headers http.Header, currentCount int) (PageState, bool) {
	if next := headerInt(headers, "X-Next-Page"); next > 0 {
		return PageState{Page: next, PerPage: p.PerPage}, true
	}
	totalPages := headerInt(headers, "X-Total-Pages")
	currentPage := headerInt(headers, "X-Page")
	if totalPages > 0 && currentPage > 0 && currentPage < totalPages {
		return PageState{Page: currentPage + 1, PerPage: p.PerPage}, true
	}
	return PageState{}, false
}

func paginationStrategy(cfg PaginationConfig) PaginationStrategy {
	if cfg.Strategy != nil {
		return cfg.Strategy
	}
	return PagePagination{Page: cfg.Page, PerPage: cfg.PerPage}
}

func headerInt(headers http.Header, name string) int {
	value, err := strconv.Atoi(headers.Get(name))
	if err != nil {
		return 0
	}
	return value
}
