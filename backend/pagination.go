package main

import (
	"net/http"
	"strconv"
)

// All interactive lists are bounded, including requests without a page.
type listPage struct{ Page, Size, Total int }

func (a *app) listPage(w http.ResponseWriter, r *http.Request, size int, countSQL string, args ...any) (listPage, bool) {
	p := listPage{Page: 1, Size: size}
	if raw := r.URL.Query().Get("page"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 {
			apiError(w, 422, "页码必须是正整数", nil)
			return p, false
		}
		p.Page = value
	}
	if err := a.db.QueryRow(r.Context(), countSQL, args...).Scan(&p.Total); err != nil {
		dbError(w, err)
		return p, false
	}
	p.Page = min(p.Page, max(1, (p.Total+p.Size-1)/p.Size))
	w.Header().Set("X-Total-Count", strconv.Itoa(p.Total))
	w.Header().Set("X-Page", strconv.Itoa(p.Page))
	w.Header().Set("X-Page-Size", strconv.Itoa(p.Size))
	return p, true
}

func (p listPage) Offset() int { return (p.Page - 1) * p.Size }
