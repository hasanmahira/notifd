package http

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/hasanmahira/notifd/internal/domain"
	"github.com/hasanmahira/notifd/internal/usecase"
)

type BatchHandler struct {
	Svc *usecase.Service
}

func NewBatchHandler(s *usecase.Service) *BatchHandler { return &BatchHandler{Svc: s} }

// batchSummaryView is the wire shape for GET /v1/batches/{batch_id}.
type batchSummaryView struct {
	BatchID   string         `json:"batch_id"`
	Total     int            `json:"total"`
	ByStatus  map[string]int `json:"by_status"`
	CreatedAt time.Time      `json:"created_at"`
}

// Get handles GET /v1/batches/{batch_id}.
func (h *BatchHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := domain.ParseID(chi.URLParam(r, "batch_id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	s, err := h.Svc.GetBatch(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	by := make(map[string]int, len(s.ByStatus))
	for k, v := range s.ByStatus {
		by[string(k)] = v
	}
	writeJSON(w, http.StatusOK, batchSummaryView{
		BatchID:   s.BatchID.String(),
		Total:     s.Total,
		ByStatus:  by,
		CreatedAt: s.CreatedAt,
	})
}
