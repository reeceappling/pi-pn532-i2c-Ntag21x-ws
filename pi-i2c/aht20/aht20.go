package aht20

import (
	"errors"
	"time"

	"github.com/d2r2/go-i2c"
)

// Device is an AHT20 device, wraps results and the I2C connection.
type Device struct {
	conn     *i2c.I2C
	humidity uint32
	temp     uint32
	*Options
}

type Options struct {
	configSleepTime, readDelay time.Duration
	nTries                     int
}

type status byte

func ternary[T any](condition bool, ifTrue, ifFalse T) T {
	if condition {
		return ifTrue
	}
	return ifFalse
}
func errIf(condition bool, err error) error {
	if condition {
		return err
	}
	return nil
}

func (s status) busy() error {
	return ternary(s&statusBusy != 0, ErrBusy, nil)
}
func (s status) calibrated() error {
	return ternary(s&statusCalibrated == 1, nil, ErrNotCalibrated)
}

func Opts() *Options {
	return &Options{
		configSleepTime: sleepConfigMin,
		readDelay:       sleepMeasureMin,
		nTries:          3,
	}
}
func (opts *Options) SetMaxTries(n int) *Options {
	if opts == nil {
		*opts = *Opts()
	}
	if n > 0 {
		opts.nTries = n
	}
	return opts
}
func (opts *Options) SetWriteReadDelay(dur time.Duration) *Options {
	if opts == nil {
		*opts = *Opts()
	}
	if dur > sleepMeasureMin {
		opts.readDelay = dur
	}
	return opts
}
func (opts *Options) SetConfigDelay(dur time.Duration) *Options {
	if opts == nil {
		*opts = *Opts()
	}
	if dur > sleepConfigMin {
		opts.configSleepTime = dur
	}
	return opts
}

func (opts *Options) sleepForConfiguration() {
	if opts == nil {
		time.Sleep(sleepConfigMin)
	} else {
		time.Sleep(opts.configSleepTime)
	}
}
func (opts *Options) sleepForMeasurement() {
	if opts == nil {
		time.Sleep(sleepMeasureMin)
	} else {
		time.Sleep(opts.readDelay)
	}
}

// New creates a new AHT20 connection.
// This function only creates the Device object, it does not touch the device.
func NewFrom(bus *i2c.I2C) Device {
	return Device{
		conn:    bus,
		Options: Opts(),
	}
}
func New(addr uint8, bus int) (Device, error) {
	conn, err := i2c.NewI2C(addr, bus)
	if err != nil {
		return Device{}, err
	}
	return NewFrom(conn), nil
}

// Configure the device
func (d *Device) Configure() error {
	err := d.CheckCalibrated()
	if err != nil && errors.Is(err, ErrNotCalibrated) {
		// Force initialization
		return d.Initialize()
	}
	return err
}

var ErrNotCalibrated = errors.New("AHT20 is not calibrated, initialization needed")

func (d *Device) CheckCalibrated() error {
	stat, err := d.status()
	if err != nil {
		return err
	}
	return stat.calibrated()
}

func (d *Device) Initialize() error {
	lg.Debug("Initializing AHT20")
	err := d.write([]byte{cmdInit, argInit1, argInit2})
	d.sleepForConfiguration()
	return err
}

// Reset the device
func (d *Device) Reset() error {
	lg.Debug("Resetting sensor...")
	return d.write([]byte{cmdSoftReset})
}

// Status of the device
func (d *Device) status() (status, error) {
	data := []byte{0}
	err := d.tx([]byte{cmdStatus}, data)
	return status(data[0]), err
}

// Read the temperature and humidity
//
// The actual temperature and humidity are stored
// and can be accessed using `Temp` and `Humidity`.
func (d *Device) Read() error {
	err := d.write([]byte{cmdMeasure, argMeasure1, argMeasure2})
	if err != nil {
		return err
	}
	data := make([]byte, 7)
	lg.Debug("Reading...")
	for retry := 0; retry < d.nTries; retry++ {
		d.sleepForMeasurement()

		lg.Debug("Trying to read")
		err := d.read(data)
		if err != nil {
			lg.Debug("Error reading: " + err.Error())
			return err
		}
		lg.Debug("Checking status")
		stat := status(data[0])
		if err := stat.busy(); err != nil {
			lg.Debug("busy, retrying...")
			continue
		}
		//if err := stat.calibrated(); err != nil { // TODO: renew
		//	lg.Debug("Error ensuring calibrated: " + err.Error())
		//	return err
		//}

		// measurement read, store values
		d.humidity = uint32(data[1])<<12 | uint32(data[2])<<4 | uint32(data[3])>>4
		d.temp = (uint32(data[3])&0xF)<<16 | uint32(data[4])<<8 | uint32(data[5]) // TODO: what is 0xF here?
		return nil
	}

	return ErrTimeout
}

// rh returns the (percentage [0-100]) relative humidity of the last-read value
func (d *Device) rh() float32 {
	return (float32(d.humidity) * 100) / 0x100000
}

// tC returns the temperature, in Celsius, last measured
func (d *Device) tC() float32 {
	return (float32(d.temp*200.0) / 0x100000) - 50
}
