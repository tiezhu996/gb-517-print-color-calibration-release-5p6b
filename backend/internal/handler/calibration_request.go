package handler

import (
	"net/http"

	"github.com/blueship581/print-color-calibration-release/backend/internal/dto"
	"github.com/blueship581/print-color-calibration-release/backend/internal/middleware"
	"github.com/blueship581/print-color-calibration-release/backend/internal/service"
	"github.com/blueship581/print-color-calibration-release/backend/internal/util"
	"github.com/gin-gonic/gin"
)

// CalibrationHandler exposes the batch colour re-calibration closed loop.
// Registration and retest backfill are reviewer-only quality actions.
type CalibrationHandler struct{ service service.CalibrationService }

func NewCalibrationHandler(s service.CalibrationService) *CalibrationHandler {
	return &CalibrationHandler{service: s}
}

func (h *CalibrationHandler) Register(group *gin.RouterGroup) {
	reviewer := middleware.RequireMinimumRole("reviewer")
	resource := group.Group("/calibrations")
	resource.GET("", h.list)
	resource.GET("/:id", h.get)
	resource.POST("", reviewer, h.create)
	resource.POST("/:id/complete", reviewer, h.complete)
}

func (h *CalibrationHandler) list(c *gin.Context) {
	var query dto.CalibrationPageQuery
	if err := c.ShouldBindQuery(&query); err != nil {
		util.Fail(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	result, err := h.service.List(c.Request.Context(), query)
	if err != nil {
		handleError(c, err)
		return
	}
	util.Page(c, result.Items, result.Page, result.PageSize, result.Total)
}

func (h *CalibrationHandler) get(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	item, err := h.service.Get(c.Request.Context(), id)
	if err != nil {
		handleError(c, err)
		return
	}
	util.OK(c, item)
}

func (h *CalibrationHandler) create(c *gin.Context) {
	var input dto.CreateCalibrationRequest
	if !bindCalibrationJSON(c, &input) {
		return
	}
	item, err := h.service.Create(c.Request.Context(), input, actorFromContext(c), roleFromContext(c), requestIDFromContext(c))
	if err != nil {
		handleError(c, err)
		return
	}
	util.Created(c, item)
}

func (h *CalibrationHandler) complete(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	var input dto.CompleteCalibrationRequest
	if !bindCalibrationJSON(c, &input) {
		return
	}
	item, err := h.service.Complete(c.Request.Context(), id, input, actorFromContext(c), roleFromContext(c), requestIDFromContext(c))
	if err != nil {
		handleError(c, err)
		return
	}
	util.OK(c, item)
}

func bindCalibrationJSON(c *gin.Context, target any) bool {
	if err := c.ShouldBindJSON(target); err != nil {
		util.Fail(c, http.StatusBadRequest, "invalid_request", err.Error())
		return false
	}
	return true
}
