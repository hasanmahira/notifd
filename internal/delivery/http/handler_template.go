package http

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/hasanmahira/notifd/internal/domain"
	"github.com/hasanmahira/notifd/internal/usecase"
)

type TemplateHandler struct {
	Svc *usecase.TemplateService
}

func NewTemplateHandler(s *usecase.TemplateService) *TemplateHandler {
	return &TemplateHandler{Svc: s}
}

type createTemplateReq struct {
	Name        string  `json:"name"`
	Channel     string  `json:"channel"`
	Locale      string  `json:"locale,omitempty"`
	SubjectTmpl *string `json:"subject_tmpl,omitempty"`
	BodyTmpl    string  `json:"body_tmpl"`
}

type templateView struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Channel     string    `json:"channel"`
	Locale      string    `json:"locale"`
	SubjectTmpl *string   `json:"subject_tmpl,omitempty"`
	BodyTmpl    string    `json:"body_tmpl"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func toTemplateView(t domain.Template) templateView {
	return templateView{
		ID:          t.ID.String(),
		Name:        t.Name,
		Channel:     string(t.Channel),
		Locale:      t.Locale,
		SubjectTmpl: t.SubjectTmpl,
		BodyTmpl:    t.BodyTmpl,
		CreatedAt:   t.CreatedAt,
		UpdatedAt:   t.UpdatedAt,
	}
}

// Create handles POST /v1/templates.
func (h *TemplateHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req createTemplateReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	t, err := h.Svc.Create(r.Context(), usecase.CreateTemplateInput{
		Name:        req.Name,
		Channel:     domain.Channel(req.Channel),
		Locale:      req.Locale,
		SubjectTmpl: req.SubjectTmpl,
		BodyTmpl:    req.BodyTmpl,
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toTemplateView(t))
}

// Get handles GET /v1/templates/{id}.
func (h *TemplateHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := domain.ParseID(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	t, err := h.Svc.Get(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toTemplateView(t))
}

// List handles GET /v1/templates.
type templateListView struct {
	Items []templateView `json:"items"`
}

func (h *TemplateHandler) List(w http.ResponseWriter, r *http.Request) {
	tmpls, err := h.Svc.List(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	out := templateListView{Items: make([]templateView, 0, len(tmpls))}
	for _, t := range tmpls {
		out.Items = append(out.Items, toTemplateView(t))
	}
	writeJSON(w, http.StatusOK, out)
}

// Update handles PUT /v1/templates/{id}. Only subject_tmpl + body_tmpl
// are mutable; name/channel/locale are immutable at this tier.
type updateTemplateReq struct {
	SubjectTmpl *string `json:"subject_tmpl,omitempty"`
	BodyTmpl    string  `json:"body_tmpl"`
}

func (h *TemplateHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, err := domain.ParseID(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	var req updateTemplateReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	t, err := h.Svc.Update(r.Context(), id, usecase.UpdateTemplateInput{
		SubjectTmpl: req.SubjectTmpl,
		BodyTmpl:    req.BodyTmpl,
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toTemplateView(t))
}

// Delete handles DELETE /v1/templates/{id}.
func (h *TemplateHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := domain.ParseID(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	if err := h.Svc.Delete(r.Context(), id); err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Preview handles POST /v1/templates/{id}/preview. Renders without persisting
// anything so clients can sanity-check variable maps. Idempotency is NOT
// required on this endpoint — it has no side effects.
type previewReq struct {
	Variables map[string]any `json:"variables"`
}

type previewResp struct {
	Subject string `json:"subject,omitempty"`
	Body    string `json:"body"`
}

func (h *TemplateHandler) Preview(w http.ResponseWriter, r *http.Request) {
	id, err := domain.ParseID(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	var req previewReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	res, err := h.Svc.Preview(r.Context(), id, req.Variables)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, previewResp{Subject: res.Subject, Body: res.Body})
}
