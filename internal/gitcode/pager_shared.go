package gitcode

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
)

type pageFetcher[T any] func(context.Context, PageState) ([]T, http.Header, error)

func getPaged[T any](ctx context.Context, c *HTTPClient, endpoint string, baseValues url.Values, initial PageState) ([]T, PageState, error) {
	strategy := paginationStrategy(c.pagination)
	fetch := func(ctx context.Context, state PageState) ([]T, http.Header, error) {
		values := cloneValues(baseValues)
		strategy.Apply(values, state)
		body, headers, err := c.getBytes(ctx, endpoint, values)
		if err != nil {
			return nil, nil, err
		}
		var pageItems []T
		if err := decodeJSON(endpoint, body, &pageItems); err != nil {
			return nil, nil, err
		}
		return pageItems, headers, nil
	}
	return collectPages(ctx, endpoint, initial, c.pagination, strategy, fetch)
}

func paginateFixture[T any](ctx context.Context, endpoint string, items []T, initial PageState, cfg PaginationConfig, scenario string) (Page[T], error) {
	strategy := paginationStrategy(cfg)
	fetch := func(ctx context.Context, state PageState) ([]T, http.Header, error) {
		if err := ctx.Err(); err != nil {
			return nil, nil, ErrNetworkUnavailable{Endpoint: endpoint, Cause: err, Attempts: 0}
		}
		if scenario == "malformed-page" {
			return nil, http.Header{"X-Page": []string{"-1"}}, nil
		}
		if scenario == "pagination-loop" {
			return slicePage(items, state, cfg), http.Header{"X-Next-Page": []string{pageString(state)}}, nil
		}
		pageItems := slicePage(items, state, cfg)
		headers := http.Header{}
		page := firstPositive(state.Page, cfg.Page, 1)
		perPage := firstPositive(state.PerPage, cfg.PerPage, len(items))
		if perPage <= 0 {
			perPage = len(items)
		}
		headers.Set("X-Page", pageString(PageState{Page: page}))
		headers.Set("X-Per-Page", pageString(PageState{Page: perPage}))
		if page*perPage < len(items) {
			headers.Set("X-Next-Page", pageString(PageState{Page: page + 1}))
		}
		return pageItems, headers, nil
	}
	collected, state, err := collectPages(ctx, endpoint, initial, cfg, strategy, fetch)
	if err != nil {
		return Page[T]{}, err
	}
	return Page[T]{Items: collected, Page: state.Page, PerPage: state.PerPage, TotalCount: len(items)}, nil
}

func collectPages[T any](ctx context.Context, endpoint string, initial PageState, cfg PaginationConfig, strategy PaginationStrategy, fetch pageFetcher[T]) ([]T, PageState, error) {
	state := initial
	if state.Page == 0 {
		state.Page = cfg.Page
	}
	if state.PerPage == 0 {
		state.PerPage = cfg.PerPage
	}
	if state.Page == 0 && state.Cursor == "" {
		state.Page = 1
	}
	if err := validatePageState(endpoint, state); err != nil {
		return nil, PageState{}, err
	}
	seen := map[PageState]bool{}
	var items []T
	for {
		if seen[state] {
			return nil, PageState{}, ErrPaginationLoop{Endpoint: endpoint, State: state}
		}
		seen[state] = true
		pageItems, headers, err := fetch(ctx, state)
		if err != nil {
			return nil, PageState{}, err
		}
		if err := validatePageMetadata(endpoint, headers); err != nil {
			return nil, PageState{}, err
		}
		items = append(items, pageItems...)
		next, ok := strategy.Next(headers, len(pageItems))
		if !ok {
			return items, state, nil
		}
		if err := validatePageState(endpoint, next); err != nil {
			return nil, PageState{}, err
		}
		if !advancesPageState(state, next) {
			return nil, PageState{}, ErrPaginationLoop{Endpoint: endpoint, State: next}
		}
		state = next
	}
}

func validatePageState(endpoint string, state PageState) error {
	if state.Page < 0 || state.PerPage < 0 {
		return ErrPaginationMalformed{Endpoint: endpoint, State: state, Message: "negative page or per_page"}
	}
	return nil
}

func validatePageMetadata(endpoint string, headers http.Header) error {
	for _, name := range []string{"X-Page", "X-Per-Page", "X-Next-Page", "X-Total-Pages"} {
		if values := headers.Values(name); len(values) > 0 {
			for _, value := range values {
				if value == "" {
					continue
				}
				parsed, err := strconv.Atoi(value)
				if err != nil || parsed < 0 {
					return ErrPaginationMalformed{Endpoint: endpoint, Message: "invalid " + name}
				}
			}
		}
	}
	return nil
}

func advancesPageState(current, next PageState) bool {
	if next.Cursor != "" {
		return next.Cursor != current.Cursor
	}
	if next.Page != 0 {
		return next.Page > current.Page
	}
	return false
}

func slicePage[T any](items []T, state PageState, cfg PaginationConfig) []T {
	page := firstPositive(state.Page, cfg.Page, 1)
	perPage := firstPositive(state.PerPage, cfg.PerPage, len(items))
	if perPage <= 0 {
		return append([]T(nil), items...)
	}
	start := (page - 1) * perPage
	if start >= len(items) {
		return []T{}
	}
	end := start + perPage
	if end > len(items) {
		end = len(items)
	}
	return append([]T(nil), items[start:end]...)
}

func pageString(state PageState) string {
	return strconv.Itoa(state.Page)
}
