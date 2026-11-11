package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/sky8791-flash/linkpulse/internal/analytics"
	"github.com/sky8791-flash/linkpulse/internal/domain"
	"github.com/sky8791-flash/linkpulse/internal/link"
)

const maxRequestBody = 1 << 20

type createLinkRequest struct {
	TargetURL   string     `json:"target_url"`
	CustomAlias string     `json:"custom_alias,omitempty"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
}

type updateLinkRequest struct {
	TargetURL *string         `json:"target_url,omitempty"`
	ExpiresAt json.RawMessage `json:"expires_at,omitempty"`
	IsActive  *bool           `json:"is_active,omitempty"`
}

type linkResponse struct {
	Code      string     `json:"code"`
	ShortURL  string     `json:"short_url"`
	TargetURL string     `json:"target_url"`
	ExpiresAt *time.Time `json:"expires_at"`
	IsActive  bool       `json:"is_active"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

type listLinksResponse struct {
	Items      []linkResponse `json:"items"`
	NextCursor *int64         `json:"next_cursor,omitempty"`
}

type errorResponse struct {
	Error apiError `json:"error"`
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (s *server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *server) ready(w http.ResponseWriter, r *http.Request) {
	if s.Ready == nil {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.Ready(ctx); err != nil {
		s.Logger.Warn("readiness check failed", "error", err)
		writeError(w, http.StatusServiceUnavailable, "not_ready", "database is not ready")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (s *server) openapi(w http.ResponseWriter, _ *http.Request) {
	data, err := embedded.ReadFile("openapi.yaml")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not load API documentation")
		return
	}
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (s *server) dashboard(w http.ResponseWriter, r *http.Request) {
	data, err := embedded.ReadFile("web/index.html")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not load dashboard")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (s *server) createLink(w http.ResponseWriter, r *http.Request) {
	var request createLinkRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	created, err := s.Links.Create(r.Context(), linkCreateInput(request))
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, s.toResponse(created))
}

func (s *server) listLinks(w http.ResponseWriter, r *http.Request) {
	cursor, err := parseIntQuery(r, "cursor", 0)
	if err != nil || cursor < 0 {
		writeError(w, http.StatusBadRequest, "invalid_cursor", "cursor must be a non-negative integer")
		return
	}
	limit, err := parseIntQuery(r, "limit", 20)
	if err != nil || limit < 1 || limit > 100 {
		writeError(w, http.StatusBadRequest, "invalid_limit", "limit must be between 1 and 100")
		return
	}
	page, err := s.Links.List(r.Context(), cursor, int(limit))
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	response := listLinksResponse{Items: make([]linkResponse, 0, len(page.Items)), NextCursor: page.NextCursor}
	for _, item := range page.Items {
		response.Items = append(response.Items, s.toResponse(item))
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *server) getLink(w http.ResponseWriter, r *http.Request) {
	found, err := s.Links.Get(r.Context(), r.PathValue("code"))
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, s.toResponse(found))
}

func (s *server) updateLink(w http.ResponseWriter, r *http.Request) {
	var request updateLinkRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if request.TargetURL == nil && len(request.ExpiresAt) == 0 && request.IsActive == nil {
		writeError(w, http.StatusBadRequest, "empty_patch", "at least one field is required")
		return
	}

	patch := domain.LinkPatch{
		TargetURL: request.TargetURL,
		IsActive:  request.IsActive,
	}
	if len(request.ExpiresAt) > 0 {
		patch.ExpiresAt.Set = true
		if !bytes.Equal(bytes.TrimSpace(request.ExpiresAt), []byte("null")) {
			var expiresAt time.Time
			if err := json.Unmarshal(request.ExpiresAt, &expiresAt); err != nil {
				writeError(w, http.StatusBadRequest, "invalid_expires_at", "expires_at must be an RFC3339 timestamp or null")
				return
			}
			patch.ExpiresAt.Value = &expiresAt
		}
	}

	updated, err := s.Links.Update(r.Context(), r.PathValue("code"), patch)
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, s.toResponse(updated))
}

func (s *server) deleteLink(w http.ResponseWriter, r *http.Request) {
	if err := s.Links.Delete(r.Context(), r.PathValue("code")); err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) linkStats(w http.ResponseWriter, r *http.Request) {
	today := s.Now().In(s.StatsLocation)
	today = time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, s.StatsLocation)
	fromDate := today.AddDate(0, 0, -6)
	toDate := today

	var err error
	if raw := r.URL.Query().Get("from"); raw != "" {
		fromDate, err = time.ParseInLocation(time.DateOnly, raw, s.StatsLocation)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_from", "from must use YYYY-MM-DD")
			return
		}
	}
	if raw := r.URL.Query().Get("to"); raw != "" {
		toDate, err = time.ParseInLocation(time.DateOnly, raw, s.StatsLocation)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_to", "to must use YYYY-MM-DD")
			return
		}
	}
	if toDate.Before(fromDate) || toDate.After(fromDate.AddDate(0, 0, 89)) {
		writeError(w, http.StatusBadRequest, "invalid_range", "stats range must be between 1 and 90 days")
		return
	}

	stats, err := s.Links.Stats(r.Context(), r.PathValue("code"), fromDate, toDate.AddDate(0, 0, 1))
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	stats.Code = r.PathValue("code")
	writeJSON(w, http.StatusOK, stats)
}

func (s *server) redirect(w http.ResponseWriter, r *http.Request) {
	code := r.PathValue("code")
	found, err := s.Links.Resolve(r.Context(), code)
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}

	referrer, device, browser := analytics.Metadata(r.UserAgent(), r.Referer())
	if s.Recorder != nil {
		s.Recorder.Record(domain.ClickEvent{
			LinkID:      found.ID,
			OccurredAt:  s.Now().UTC(),
			VisitorHash: analytics.VisitorHash(s.VisitorHashSecret, clientIP(r)),
			Referrer:    referrer,
			Device:      device,
			Browser:     browser,
		})
	}

	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Robots-Tag", "noindex")
	http.Redirect(w, r, found.TargetURL, http.StatusFound)
}

func (s *server) toResponse(link domain.Link) linkResponse {
	return linkResponse{
		Code:      link.Code,
		ShortURL:  strings.TrimRight(s.BaseURL, "/") + "/" + url.PathEscape(link.Code),
		TargetURL: link.TargetURL,
		ExpiresAt: link.ExpiresAt,
		IsActive:  link.IsActive,
		CreatedAt: link.CreatedAt.UTC(),
		UpdatedAt: link.UpdatedAt.UTC(),
	}
}

func (s *server) writeServiceError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, domain.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "link not found")
	case errors.Is(err, domain.ErrExpired), errors.Is(err, domain.ErrInactive):
		writeError(w, http.StatusGone, "link_gone", "link is no longer available")
	case errors.Is(err, domain.ErrConflict):
		writeError(w, http.StatusConflict, "alias_conflict", "short code is already in use")
	case errors.Is(err, domain.ErrInvalidTargetURL):
		writeError(w, http.StatusBadRequest, "invalid_target_url", "target_url must be an absolute http or https URL up to 2048 characters")
	case errors.Is(err, domain.ErrInvalidAlias):
		writeError(w, http.StatusBadRequest, "invalid_alias", "custom_alias must be 4-32 letters, digits, underscores, or hyphens and must not be reserved")
	case errors.Is(err, domain.ErrInvalidExpiration):
		writeError(w, http.StatusBadRequest, "invalid_expiration", "expires_at must be in the future")
	default:
		s.Logger.Error("request failed",
			"error", err,
			"request_id", requestIDFrom(r),
		)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
	}
}

func linkCreateInput(request createLinkRequest) link.CreateInput {
	return link.CreateInput{
		TargetURL:   request.TargetURL,
		CustomAlias: request.CustomAlias,
		ExpiresAt:   request.ExpiresAt,
	}
}

func decodeJSON(w http.ResponseWriter, r *http.Request, destination any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("invalid JSON body: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain exactly one JSON object")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(value)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorResponse{Error: apiError{Code: code, Message: message}})
}

func parseIntQuery(r *http.Request, key string, fallback int64) (int64, error) {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return fallback, nil
	}
	return strconv.ParseInt(raw, 10, 64)
}
