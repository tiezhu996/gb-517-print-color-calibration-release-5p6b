package dto

import "time"

// CreateCalibrationRequest registers a batch colour re-calibration while the
// print run is in proofing. The reviewer records the measuring equipment,
// target colour difference (ΔE), retest samples and the retest deadline.
type CreateCalibrationRequest struct {
	PrintRunID  uint      `json:"printRunId" binding:"required"`
	PressUnitID uint      `json:"pressUnitId" binding:"required"`
	TargetDelta float64   `json:"targetDelta" binding:"gte=0,lte=100"`
	Samples     string    `json:"samples" binding:"required,min=2,max=2000"`
	RetestDueAt time.Time `json:"retestDueAt" binding:"required"`
}

// CompleteCalibrationRequest backfills the retest evidence. Result must agree
// with measuredDelta compared against the registered target delta; expected
// version provides optimistic locking so concurrent or repeated completion
// can only succeed once.
type CompleteCalibrationRequest struct {
	ExpectedVersion uint    `json:"expectedVersion" binding:"required"`
	MeasuredDelta   float64 `json:"measuredDelta" binding:"gte=0,lte=100"`
	Result          string  `json:"result" binding:"required,oneof=passed failed"`
	ResultNote      string  `json:"resultNote" binding:"max=2000"`
}

// CalibrationPageQuery extends paging with run and state filters.
type CalibrationPageQuery struct {
	Page       int    `form:"page"`
	PageSize   int    `form:"pageSize"`
	Status     string `form:"status"`
	PrintRunID uint   `form:"printRunId"`
}
