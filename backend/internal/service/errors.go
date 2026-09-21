package service

import "errors"

var (
	ErrInvalidTransition  = errors.New("requested status transition is not allowed")
	ErrInvalidInput       = errors.New("business input validation failed")
	ErrUnauthorized       = errors.New("invalid username or password")
	ErrInactiveUser       = errors.New("user account is inactive")
	ErrForbidden          = errors.New("role is not permitted for this operation")
	ErrLocked             = errors.New("resolved record is immutable")
	ErrDuplicatePending   = errors.New("print run already has a pending calibration request")
	ErrPressUnavailable   = errors.New("selected press unit is unavailable for calibration")
	ErrInvalidDueDate     = errors.New("retest due date must be in the future")
	ErrRunNotProofing     = errors.New("calibration can only be scheduled or resolved while the run is in proofing")
	ErrCalibrationBlocked = errors.New("pending or failed calibration blocks this run action")
)
