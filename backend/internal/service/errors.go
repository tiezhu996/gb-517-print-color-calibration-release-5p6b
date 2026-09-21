package service

import "errors"

var (
	ErrInvalidTransition  = errors.New("requested status transition is not allowed")
	ErrInvalidInput       = errors.New("business input validation failed")
	ErrUnauthorized       = errors.New("invalid username or password")
	ErrInactiveUser       = errors.New("user account is inactive")
	ErrForbidden          = errors.New("role is not permitted for this operation")
	ErrLocked             = errors.New("resolved record is immutable")
	ErrPendingCalibration = errors.New("batch already has a pending calibration request")
	ErrPressUnavailable   = errors.New("measuring press unit is not available")
	ErrInvalidDeadline    = errors.New("retest deadline must be in the future")
	ErrCalibrationSettled = errors.New("calibration retest was already backfilled")
	ErrResultMismatch     = errors.New("result does not match measured delta versus target delta")
	ErrReleaseBlocked     = errors.New("batch release is blocked by an open calibration")
)
