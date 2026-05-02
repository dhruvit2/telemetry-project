package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gorilla/mux"
	"go.uber.org/zap"

	"telemetry-api/pkg/repository"
)

// ErrorResponse represents an error response
type ErrorResponse struct {
	Error     string `json:"error"`
	Timestamp string `json:"timestamp"`
	Code      int    `json:"code"`
}

// GPUListResponse represents the response containing a list of GPUs
type GPUListResponse struct {
	GPUs  []repository.GPU `json:"gpus"`
	Count int              `json:"count"`
}

// GPUTelemetryResponse represents the response containing telemetry data
type GPUTelemetryResponse struct {
	GPUID     string                 `json:"gpu_id"`
	Telemetry []repository.Telemetry `json:"telemetry"`
	Count     int                    `json:"count"`
	Limit     int                    `json:"limit"`
	Offset    int                    `json:"offset"`
	StartDate string                 `json:"start_date,omitempty"`
	EndDate   string                 `json:"end_date,omitempty"`
}

// GPUHandler handles GPU-related HTTP endpoints
type GPUHandler struct {
	repo   *repository.TSDBRepository
	logger *zap.Logger
}

// NewGPUHandler creates a new GPU handler
func NewGPUHandler(repo *repository.TSDBRepository, logger *zap.Logger) *GPUHandler {
	return &GPUHandler{
		repo:   repo,
		logger: logger,
	}
}

// GetGPUs returns a list of all GPUs
// @Summary List all GPU IDs
// @Description Retrieve a list of all GPUs that have reported telemetry
// @Tags GPUs
// @Accept json
// @Produce json
// @Success 200 {object} GPUListResponse
// @Failure 500 {object} ErrorResponse
// @Router /api/v1/gpus [get]
func (h *GPUHandler) GetGPUs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	h.logger.Debug("GetGPUs request received")

	gpus, err := h.repo.GetGPUs(ctx)
	if err != nil {
		h.logger.Error("failed to retrieve GPUs", zap.Error(err))
		writeErrorResponse(w, http.StatusInternalServerError, fmt.Sprintf("failed to retrieve GPUs: %v", err))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	response := GPUListResponse{
		GPUs:  gpus,
		Count: len(gpus),
	}

	if err := json.NewEncoder(w).Encode(response); err != nil {
		h.logger.Error("failed to write response", zap.Error(err))
	}

	h.logger.Debug("GetGPUs request completed", zap.Int("count", len(gpus)))
}

// GetGPUTelemetry returns telemetry data for a specific GPU
// @Summary Get telemetry data for a GPU
// @Description Retrieve telemetry data for a specific GPU ID within an optional date range
// @Tags GPUs
// @Accept json
// @Produce json
// @Param id path string true "GPU ID"
// @Param start_date query string false "Start Date (RFC3339 or YYYY-MM-DD)"
// @Param end_date query string false "End Date (RFC3339 or YYYY-MM-DD)"
// @Param limit query int false "Number of records to return (default 100, max 1000)"
// @Param offset query int false "Number of records to skip (default 0)"
// @Success 200 {object} GPUTelemetryResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /api/v1/gpus/{id}/telemetry [get]
func (h *GPUHandler) GetGPUTelemetry(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	vars := mux.Vars(r)
	gpuID := vars["id"]

	h.logger.Debug("GetGPUTelemetry request received", zap.String("gpu_id", gpuID))

	// Parse optional start_date and end_date query parameters
	startDateStr := r.URL.Query().Get("start_date")
	endDateStr := r.URL.Query().Get("end_date")

	var startDate, endDate *time.Time

	if startDateStr != "" {
		t, err := parseDate(startDateStr)
		if err != nil {
			h.logger.Warn("invalid start_date format", zap.String("start_date", startDateStr), zap.Error(err))
			writeErrorResponse(w, http.StatusBadRequest, fmt.Sprintf("invalid start_date format: %v", err))
			return
		}
		startDate = &t
	}

	if endDateStr != "" {
		t, err := parseDate(endDateStr)
		if err != nil {
			h.logger.Warn("invalid end_date format", zap.String("end_date", endDateStr), zap.Error(err))
			writeErrorResponse(w, http.StatusBadRequest, fmt.Sprintf("invalid end_date format: %v", err))
			return
		}
		endDate = &t
	}

	// Parse limit and offset parameters
	limit := 100 // default limit
	limitStr := r.URL.Query().Get("limit")
	if limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 {
			if l > 1000 {
				limit = 1000 // max limit
			} else {
				limit = l
			}
		}
	}

	offset := 0 // default offset
	offsetStr := r.URL.Query().Get("offset")
	if offsetStr != "" {
		if o, err := strconv.Atoi(offsetStr); err == nil && o >= 0 {
			offset = o
		}
	}

	// Retrieve telemetry data
	telemetry, err := h.repo.GetGPUTelemetryByDateRange(ctx, gpuID, startDate, endDate, limit, offset)
	if err != nil {
		h.logger.Error("failed to retrieve telemetry", zap.String("gpu_id", gpuID), zap.Error(err))
		writeErrorResponse(w, http.StatusInternalServerError, fmt.Sprintf("failed to retrieve telemetry: %v", err))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	response := GPUTelemetryResponse{
		GPUID:     gpuID,
		Telemetry: telemetry,
		Count:     len(telemetry),
		Limit:     limit,
		Offset:    offset,
	}

	if startDate != nil {
		response.StartDate = startDate.Format(time.RFC3339)
	}
	if endDate != nil {
		response.EndDate = endDate.Format(time.RFC3339)
	}

	if err := json.NewEncoder(w).Encode(response); err != nil {
		h.logger.Error("failed to write response", zap.Error(err))
	}

	h.logger.Debug("GetGPUTelemetry request completed", zap.String("gpu_id", gpuID), zap.Int("count", len(telemetry)))
}

// Helper functions

// writeErrorResponse writes an error response
func writeErrorResponse(w http.ResponseWriter, statusCode int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)

	response := ErrorResponse{
		Error:     message,
		Timestamp: time.Now().Format(time.RFC3339Nano),
		Code:      statusCode,
	}

	json.NewEncoder(w).Encode(response)
}

// parseDate parses a date string in RFC3339 or ISO8601 format
func parseDate(dateStr string) (time.Time, error) {
	// Try RFC3339 format first
	if t, err := time.Parse(time.RFC3339, dateStr); err == nil {
		return t, nil
	}

	// Try RFC3339Nano format
	if t, err := time.Parse(time.RFC3339Nano, dateStr); err == nil {
		return t, nil
	}

	// Try simple date format (YYYY-MM-DD)
	if t, err := time.Parse("2006-01-02", dateStr); err == nil {
		return t, nil
	}

	// Try datetime format (YYYY-MM-DD HH:MM:SS)
	if t, err := time.Parse("2006-01-02 15:04:05", dateStr); err == nil {
		return t, nil
	}

	return time.Time{}, fmt.Errorf("invalid date format: %s", dateStr)
}
