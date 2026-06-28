package aht20

import (
	"errors"
	"time"
)

const (
	cmdInit  = 0xBE
	argInit1 = 0x08
	argInit2 = 0x00

	cmdMeasure  = 0xAC
	argMeasure1 = 0x33
	argMeasure2 = 0x00

	cmdSoftReset = 0xBA

	cmdStatus = 0x71

	statusBusy       = 0x80
	statusCalibrated = 0x08

	sleepConfigMin  = 10 * time.Millisecond
	sleepMeasureMin = 80 * time.Millisecond
)

var (
	ErrBusy    = errors.New("AHT20 busy")
	ErrTimeout = errors.New("timeout")
)
