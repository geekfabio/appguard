package control

import (
	"encoding/csv"
	"net/http"
	"strconv"
	"time"

	"appguard/internal/storage"
)

// handleEvents returns stored events, filtered by agentId, group, action and
// a time window. With format=csv it streams a CSV download instead of JSON.
func (s *controllerState) handleEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	filter := storage.EventFilter{
		AgentID: q.Get("agentId"),
		Group:   q.Get("group"),
		Action:  q.Get("action"),
		Limit:   atoiDefault(q.Get("limit"), 300),
	}
	if since := q.Get("sinceHours"); since != "" {
		if h, err := strconv.Atoi(since); err == nil && h > 0 {
			filter.Since = time.Now().Add(-time.Duration(h) * time.Hour)
		}
	}

	records, err := s.db.ListEvents(filter)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if q.Get("format") == "csv" {
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", "attachment; filename=appguard-events.csv")
		cw := csv.NewWriter(w)
		_ = cw.Write([]string{"time", "agentId", "group", "action", "app", "detail"})
		for _, e := range records {
			_ = cw.Write([]string{
				e.Time.Format(time.RFC3339), e.AgentID, e.Group, e.Action, e.AppName, e.Detail,
			})
		}
		cw.Flush()
		return
	}

	writeJSON(w, toEvents(records))
}

func atoiDefault(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil && n > 0 {
		return n
	}
	return def
}
