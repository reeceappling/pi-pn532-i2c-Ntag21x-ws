package gy302

import (
	"bytes"
	"encoding/binary"
	"errors"
	"github.com/d2r2/go-logger"
	"time"

	i2c "github.com/d2r2/go-i2c"
	"github.com/davecgh/go-spew/spew"
)

type measurementFrequency int
type resolution int

func measurementCommand(freq measurementFrequency, res resolution) int {
	return int(freq) + int(res)
}

// Command bytes
const (
	continuousMeasurement measurementFrequency = 0x10
	momentaryMeasurement  measurementFrequency = 0x20
	highRes               resolution           = 0x00
	highRes2              resolution           = 0x01
	loweRes               resolution           = 0x03
	// Shutdown sensor
	cmdShutdown = 0x00

	// Waiting for measurement command.
	cmdTurnOn = 0x01

	// Reset Data register value.
	// Reset command is invalid in powered-off state
	cmdReset = 0x07

	// Start measurement at 1lx resolution.
	// Measurement Time is typically 120ms.
	cmdMeasureContinuousHighRes = 0x10

	// Start measurement at 0.5lx resolution.
	// Measurement Time is typically 120ms.
	cmdMeasureContinuousHighRes2 = 0x11

	// Start measurement at 4lx resolution.
	// Measurement Time is typically 16ms.
	cmdMeasureContinuousLowRes = 0x13

	// Start measurement at 1lx resolution.
	// Measurement Time is typically 120ms.
	// It is automatically set to Power Down mode after measurement
	cmdMeasureMomentaryHighRes = 0x20

	// Start measurement at 0.5lx resolution.
	// Measurement Time is typically 120ms.
	// It is automatically set to Power Down mode after measurement.
	cmdMeasureMomentaryHighRes2 = 0x21

	// Start measurement at 4lx resolution.
	// Measurement Time is typically 16ms.
	// It is automatically set to Power Down mode after measurement.
	cmdMeasureMomentaryLowRes = 0x23

	// Change measurement time. 01000_MT[7,6,5]
	cmdChangeMeasurementTimeHigh = 0x40 // TODO: what to do with this?

	// Change measurement time. 011_MT[4,3,2,1,0]
	cmdChangeMeasurementTimeLow = 0x60 // TODO: what to do with this?
	defaultSensitivityFactor    = 69
	minSensitivityFactor        = 31
	maxSensitivityFactor        = 254
)

// ResolutionMode define sensor sensitivity
// and measure time. Be aware, that improving
// sensitivity lead to increasing of measurement time.
type ResolutionMode int

const (
	// LowResolution precision 4 lx, 16 ms measurement time
	LowResolution ResolutionMode = iota + 1
	// HighResolution precision 1 lx, 120 ms measurement time
	HighResolution
	// HighestResolution precision 0.5 lx, 120 ms measurement time
	HighestResolution
)

// String define stringer interface.
func (v ResolutionMode) String() string {
	switch v {
	case LowResolution:
		return "Low Resolution"
	case HighResolution:
		return "High Resolution"
	case HighestResolution:
		return "Highest Resolution"
	default:
		return "<unknown>"
	}
}

// Device it's a sensor itself.
type Device struct {
	conn *i2c.I2C
	// Since this sensor has no register to report current state,
	// we save the last issued command
	lastCmd        byte
	lastResolution ResolutionMode
	factor         byte
}

// NewFrom returns a new GY302 sensor instance.
func NewFrom(i2c *i2c.I2C) *Device {
	v := &Device{conn: i2c}
	v.factor = v.GetDefaultSensivityFactor()
	return v
}

// New returns a new GY302 sensor instance.
func New(addr uint8, bus int) (*Device, error) {
	conn, err := i2c.NewI2C(addr, bus)
	if err != nil {
		return &Device{}, err
	}
	return NewFrom(conn), nil
}

func (v *Device) doBasicCommand(cmd byte) error {
	if _, err := v.conn.WriteBytes([]byte{cmd}); err != nil {
		return err
	}
	v.lastCmd = cmd
	return nil
}

// Reset clear ambient light register value.
func (v *Device) Reset() error {
	lg.Debug("Resetting sensor...")
	return v.doBasicCommand(cmdReset)
}

// PowerDown return register to idle state.
func (v *Device) PowerDown() error {
	lg.Debug("Powering down sensor...")
	return v.doBasicCommand(cmdShutdown)
}

// PowerOn activate sensor.
func (v *Device) PowerOn() error {
	lg.Debug("Powering on sensor...")
	return v.doBasicCommand(cmdTurnOn)
}

// Get internal parameters used to program sensor.
func (v *Device) getResolutionData(resolution ResolutionMode) (cmd byte,
	wait time.Duration, divider uint32) {

	switch resolution {
	case LowResolution:
		cmd = cmdMeasureMomentaryLowRes
		divider = 1
		// typical measure time is 16 ms,
		// but as it was found 24 ms max time
		// gives better results
		wait = time.Millisecond * 24
	case HighResolution:
		cmd = cmdMeasureMomentaryHighRes
		divider = 1
		// typical measure time
		wait = time.Millisecond * 120
	case HighestResolution:
		cmd = cmdMeasureMomentaryHighRes2
		divider = 2
		// typical measure time
		wait = time.Millisecond * 120
	default:
		panic("bad resolution mode provided")
	}
	wait = wait * time.Duration(v.factor) /
		time.Duration(v.GetDefaultSensivityFactor())
	return
}

// MeasureAmbientLight measure and return ambient light once in lux.
func (v *Device) MeasureAmbientLight(resolution ResolutionMode) (uint16, error) {
	lg.Debug("Run one time measure...")
	cmd, wait, divider := v.getResolutionData(resolution)
	v.lastCmd = cmd
	v.lastResolution = resolution

	_, err := v.conn.WriteBytes([]byte{cmd})
	if err != nil {
		return 0, err
	}

	time.Sleep(wait)

	//data, err := v.readDataTo2Bytes()
	//if err != nil {
	//	return 0, err
	//}
	//return calcAmbientLight(data, divider), nil
	var data struct {
		Data [2]byte
	}
	err = readDataToStruct(v.conn, 2, binary.BigEndian, &data)
	if err != nil {
		return 0, err
	}

	return calcAmbientLight(data.Data, divider), nil
}

// StartMeasureAmbientLightContinuously start continuous
// measurement process. Use FetchMeasuredAmbientLight to get
// average ambient light amount collected and calculated over a time.
// Use PowerDown to stop measurements and return sensor to idle state.
func (v *Device) StartMeasureAmbientLightContinuously( // TODO: does this even work????
	resolution ResolutionMode) (wait time.Duration, err error) {

	lg.Debug("Start measures continuously...")

	cmd, wait, _ := v.getResolutionData(resolution)

	v.lastCmd = cmd
	v.lastResolution = resolution

	_, err = v.conn.WriteBytes([]byte{cmd})
	if err != nil {
		return 0, err
	}

	// Wait first time to collect necessary
	// amount of light for correct results.
	// It's not necessary to wait the same amount of time next time because
	// the sensor accumulates average lux amount
	// without overwriting the old value
	time.Sleep(wait)

	// In any case we are returning same
	// recommended amount of time to wait
	// between measures.
	return wait, nil
}

var ErrCmdMismatch = errors.New("last command does not match current command")

// FetchMeasuredAmbientLight return current average ambient light in lux.
// Previous command should be any continuous measurement initiation,
// otherwise error will be reported.
func (v *Device) FetchMeasuredAmbientLight() (uint16, error) {
	lg.Debug("Fetching measured data...")
	cmd, _, divider := v.getResolutionData(v.lastResolution)
	if v.lastCmd != cmd {
		return 0, errors.Join(errors.New("can't fetch measured ambient light"), ErrCmdMismatch)
	}
	data, err := v.readDataTo2Bytes()
	if err != nil {
		return 0, err
	}
	return calcAmbientLight(data, divider), nil
	//var data struct {
	//	Data [2]byte
	//}
	//err := readDataToStruct(i2c, 2, binary.BigEndian, &data)
	//if err != nil {
	//	return 0, err
	//}
	//
	//return calcAmbientLight(data.Data, divider), nil
}

func calcAmbientLight(data [2]byte, divider uint32) uint16 { // TODO: rename?
	//reading16 := uint16(data[0])<<8 | uint16(data[1])
	//calculated32 := uint32(reading16) * 5 / 6 / divider // TODO: is 5/6 not just /30 here???? Order of operations
	//calculated32 := (uint32(reading16) * 5) / (6 * divider)
	//return uint16(calculated32)
	return uint16((uint32(uint16(data[0])<<8|uint16(data[1])) * 5) / (6 * divider))

}

// GetDefaultSensivityFactor return factor value
// used when your sensor have no any protection cover.
// This is default setting according to specification.
func (v *Device) GetDefaultSensivityFactor() byte {
	return defaultSensitivityFactor
}

// ChangeSensivityFactor used when you close sensor
// with protection cover, which change (ordinary decrease)
// expected amount of light falling on the sensor.
// In this case you should calibrate you sensor and find
// appropriate factor to get in output correct ambient light value.
// Be aware, that improving sensitivity will increase
// measurement time.
func (v *Device) ChangeSensivityFactor(factor byte) error {

	lg.Debug("Changing sensitivity factor...")

	if factor < minSensitivityFactor || factor > maxSensitivityFactor {
		return errors.New(spew.Sprintf("sensitivity factor %d value exceed range [%d..%d]",
			factor, minSensitivityFactor, maxSensitivityFactor))
	}

	// write high side
	highFactor := (factor & 0xE0) >> 5 // TODO: what is this factor???
	_, err := v.conn.WriteBytes([]byte{cmdChangeMeasurementTimeHigh | highFactor})
	if err != nil {
		return err
	}
	// write low side
	lowFactor := factor & 0x1F // TODO: what is this factor???
	_, err = v.conn.WriteBytes([]byte{cmdChangeMeasurementTimeLow | lowFactor})
	if err != nil {
		return err
	}
	v.factor = factor
	return nil
}

// Read byte block from i2c device to struct object.
func (v *Device) readDataTo2Bytes() ([2]byte, error) { // TODO: this should probably not read to a struct, just output a [2]byte?
	var data struct {
		Data [2]byte
	}
	buf1 := make([]byte, 2)
	if _, err := v.conn.ReadBytes(buf1); err != nil {
		return data.Data, err
	}
	buf := bytes.NewBuffer(buf1)
	if err := binary.Read(buf, binary.BigEndian, &data); err != nil {
		return data.Data, err
	}
	return data.Data, nil
}

// Read byte block from i2c device to struct object.
func readDataToStruct(i2c *i2c.I2C, byteCount int, byteOrder binary.ByteOrder, obj interface{}) error { // TODO: this should probably not read to a struct, just output a [2]byte?
	buf1 := make([]byte, byteCount)
	_, err := i2c.ReadBytes(buf1)
	if err != nil {
		return err
	}
	buf := bytes.NewBuffer(buf1)
	err = binary.Read(buf, byteOrder, obj)
	if err != nil {
		return err
	}
	return nil
} // TODO: likely delete?

// You can manage verbosity of log output
// in the package by changing last parameter value.
var lg = logger.NewPackageLogger("gy302",
	logger.DebugLevel,
	// logger.InfoLevel,
)
