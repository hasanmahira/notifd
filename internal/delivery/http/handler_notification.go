package http

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/hasanmahira/notifd/internal/domain"
	"github.com/hasanmahira/notifd/internal/usecase"
)

type NotificationHandler struct {
	Svc *usecase.Service
}

func NewNotificationHandler(s *usecase.Service) *NotificationHandler {
	return &NotificationHandler{Svc: s}
}

// createReq is the wire format for POST /v1/notifications. Either `content`
// OR (`template_id` + `variables`) must be supplied, not both.
type createReq struct {
	Recipient   string          `json:"recipient"`
	Channel     string          `json:"channel"`
	Priority    string          `json:"priority"`
	Content     json.RawMessage `json:"content,omitempty"`
	TemplateID  *string         `json:"template_id,omitempty"`
	Variables   map[string]any  `json:"variables,omitempty"`
	ScheduledAt *time.Time      `json:"scheduled_at,omitempty"`
}

// notificationView is the wire format for any notification response.
type notificationView struct {
	ID                string     `json:"id"`
	BatchID           *string    `json:"batch_id,omitempty"`
	TemplateID        *string    `json:"template_id,omitempty"`
	Channel           string     `json:"channel"`
	Priority          string     `json:"priority"`
	Recipient         string     `json:"recipient"`
	Status            string     `json:"status"`
	Attempts          int        `json:"attempts"`
	LastError         *string    `json:"last_error,omitempty"`
	ProviderMessageID *string    `json:"provider_message_id,omitempty"`
	ScheduledAt       *time.Time `json:"scheduled_at,omitempty"`
	SentAt            *time.Time `json:"sent_at,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

func toView(n domain.Notification) notificationView {
	v := notificationView{
		ID:                n.UserNotification.ID.String(),
		Channel:           string(n.Message.Channel),
		Priority:          string(n.Message.Priority),
		Recipient:         string(n.UserNotification.Recipient),
		Status:            string(n.UserNotification.Status),
		Attempts:          n.UserNotification.Attempts,
		LastError:         n.UserNotification.LastError,
		ProviderMessageID: n.UserNotification.ProviderMessageID,
		ScheduledAt:       n.Message.ScheduledAt,
		SentAt:            n.UserNotification.SentAt,
		CreatedAt:         n.UserNotification.CreatedAt,
		UpdatedAt:         n.UserNotification.UpdatedAt,
	}
	if n.Message.BatchID != nil {
		s := n.Message.BatchID.String()
		v.BatchID = &s
	}
	if n.Message.TemplateID != nil {
		s := n.Message.TemplateID.String()
		v.TemplateID = &s
	}
	return v
}

// Create handles POST /v1/notifications.
func (h *NotificationHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req createReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	in := usecase.CreateNotificationInput{
		IdempotencyKey: r.Header.Get("Idempotency-Key"),
		Recipient:      domain.Recipient(req.Recipient),
		Channel:        domain.Channel(req.Channel),
		Priority:       domain.Priority(req.Priority),
		Content:        domain.Content(req.Content),
		Variables:      req.Variables,
		ScheduledAt:    req.ScheduledAt,
	}
	if req.TemplateID != nil && *req.TemplateID != "" {
		id, err := domain.ParseID(*req.TemplateID)
		if err != nil {
			writeError(w, r, err)
			return
		}
		in.TemplateID = &id
	}
	n, err := h.Svc.Create(r.Context(), in)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toView(n))
}

// batchReq is the wire format for POST /v1/notifications/batch.
type batchReq struct {
	Channel  string          `json:"channel"`
	Priority string          `json:"priority"`
	Content  json.RawMessage `json:"content"`
	Items    []batchItemReq  `json:"items"`
}

type batchItemReq struct {
	Recipient      string `json:"recipient"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

type batchView struct {
	BatchID  string `json:"batch_id"`
	Accepted int    `json:"accepted"`
}

// CreateBatch handles POST /v1/notifications/batch.
func (h *NotificationHandler) CreateBatch(w http.ResponseWriter, r *http.Request) {
	var req batchReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	items := make([]usecase.BatchItemInput, 0, len(req.Items))
	for _, it := range req.Items {
		items = append(items, usecase.BatchItemInput{
			Recipient:      domain.Recipient(it.Recipient),
			IdempotencyKey: it.IdempotencyKey,
		})
	}
	res, err := h.Svc.CreateBatch(r.Context(), usecase.CreateBatchInput{
		IdempotencyKey: r.Header.Get("Idempotency-Key"),
		Channel:        domain.Channel(req.Channel),
		Priority:       domain.Priority(req.Priority),
		Content:        domain.Content(req.Content),
		Items:          items,
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, batchView{
		BatchID:  res.BatchID.String(),
		Accepted: res.Accepted,
	})
}

// Get handles GET /v1/notifications/{id}.
func (h *NotificationHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := domain.ParseID(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	n, err := h.Svc.Get(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toView(n))
}

// Cancel handles POST /v1/notifications/{id}/cancel.
func (h *NotificationHandler) Cancel(w http.ResponseWriter, r *http.Request) {
	id, err := domain.ParseID(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	n, err := h.Svc.Cancel(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toView(n))
}

// List handles GET /v1/notifications?status=&channel=&from=&to=&cursor=&limit=.
type listView struct {
	Items      []notificationView `json:"items"`
	NextCursor string             `json:"next_cursor,omitempty"`
}

func (h *NotificationHandler) List(w http.ResponseWriter, r *http.Request) {
	q := usecase.ListQuery{Cursor: r.URL.Query().Get("cursor")}
	if s := r.URL.Query().Get("status"); s != "" {
		st := domain.Status(s)
		q.Status = &st
	}
	if c := r.URL.Query().Get("channel"); c != "" {
		ch := domain.Channel(c)
		q.Channel = &ch
	}
	if f := r.URL.Query().Get("from"); f != "" {
		t, err := time.Parse(time.RFC3339, f)
		if err != nil {
			writeError(w, r, fmt.Errorf("%w: from must be RFC3339", domain.ErrInvalidInput))
			return
		}
		q.From = &t
	}
	if to := r.URL.Query().Get("to"); to != "" {
		t, err := time.Parse(time.RFC3339, to)
		if err != nil {
			writeError(w, r, fmt.Errorf("%w: to must be RFC3339", domain.ErrInvalidInput))
			return
		}
		q.To = &t
	}
	if l := r.URL.Query().Get("limit"); l != "" {
		var n int
		_, err := fmt.Sscan(l, &n)
		if err != nil {
			writeError(w, r, fmt.Errorf("%w: limit must be int", domain.ErrInvalidInput))
			return
		}
		q.Limit = n
	}

	items, next, err := h.Svc.List(r.Context(), q)
	if err != nil {
		writeError(w, r, err)
		return
	}
	out := listView{Items: make([]notificationView, 0, len(items)), NextCursor: next}
	for _, n := range items {
		out.Items = append(out.Items, toView(n))
	}
	writeJSON(w, http.StatusOK, out)
}
