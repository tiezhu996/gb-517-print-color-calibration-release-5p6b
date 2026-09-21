package handler

import (
	"net/http"

	"github.com/blueship581/print-color-calibration-release/backend/internal/dto"
	"github.com/blueship581/print-color-calibration-release/backend/internal/middleware"
	"github.com/blueship581/print-color-calibration-release/backend/internal/service"
	"github.com/blueship581/print-color-calibration-release/backend/internal/util"
	"github.com/gin-gonic/gin"
)

// CalibrationRequestHandler exposes the batch colour re-calibration closed
// loop. Only reviewer/admin may schedule or resolve a request; viewers retain
// read access so the run/proof/release pages can show the evidence.
type CalibrationRequestHandler struct {
	service service.CalibrationService
}

func NewCalibrationRequestHandler(s service.CalibrationService) *CalibrationRequestHandler {
	return &CalibrationRequestHandler{service: s}
}

func (h *CalibrationRequestHandler) Register(group *gin.RouterGroup) {
	resource := group.Group("/calibrations")
	resource.GET("", h.list)
	resource.GET("/:id", h.get)
	resource.POST("", middleware.RequireMinimumRole("reviewer"), h.create)
	resource.POST("/:id/resolve", middleware.RequireMinimumRole("reviewer"), h.resolve)
}

func (h *CalibrationRequestHandler) list(c *gin.Context) {
	var query dto.CalibrationQuery
	_ = c.ShouldBindQuery(&query)
	if query.Page < 1 {
		query.Page = 1
	}
	if query.PageSize < 1 || query.PageSize > 100 {
		query.PageSize = 20
	}
	result, err := h.service.List(c.Request.Context(), query)
	if err != nil {
		handleError(c, err)
		return
	}
	util.Page(c, result.Items, result.Page, result.PageSize, result.Total)
}

func (h *CalibrationRequestHandler) get(c *gin.Context) {
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

func (h *CalibrationRequestHandler) create(c *gin.Context) {
	var input dto.CreateCalibrationRequest
	if err := c.ShouldBindJSON(&input); err != nil {
		util.Fail(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	item, err := h.service.Create(c.Request.Context(), input, actorFromContext(c), requestIDFromContext(c))
	if err != nil {
		handleError(c, err)
		return
	}
	util.Created(c, item)
}

func (h *CalibrationRequestHandler) resolve(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	var input dto.CompleteCalibrationRequest
	if err := c.ShouldBindJSON(&input); err != nil {
		util.Fail(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	item, err := h.service.Resolve(c.Request.Context(), id, input, actorFromContext(c), requestIDFromContext(c))
	if err != nil {
		handleError(c, err)
		return
	}
	util.OK(c, item)
}
