package aht20

import (
	"github.com/d2r2/go-i2c"
	"github.com/d2r2/go-logger"
)

func (d *Device) tx(w, r []byte) error {
	if len(w) > 0 {
		if err := d.write(w); err != nil {
			return err
		}
	}
	if len(r) > 0 {
		if err := d.read(r); err != nil {
			return err
		}
	}

	return nil
}

func (d *Device) read(r []byte) error {
	_, err := d.conn.ReadBytes(r)
	return err
}
func (d *Device) write(w []byte) error {
	_, err := d.conn.WriteBytes(w)
	return err
}

// NewAHT20 return new sensor instance.
func NewAHT20(bus *i2c.I2C) (Device, error) {
	aht20 := NewFrom(bus)
	//aht20.Reset() // TODO: ????
	err := aht20.Configure() // TODO: ????

	return aht20, err
}

// LastMeasuredValues gets the most recently measured values for both T and RH
func (d *Device) LastMeasuredValues() (float32, float32) {
	return d.rh(), d.tC()
}

// LastMeasuredRH returns the most recently measured value of the relative humidity [0-100]
func (d *Device) LastMeasuredRH() float32 {
	return d.rh()
}

// LastMeasuredT returns the most recently measured value of the temperature (in Celsius)
func (d *Device) LastMeasuredT() float32 {
	return d.tC()
}

// SenseAll measures, then calculates and outputs the Temperature (C) and Relative Humidity [0-100](%)
func (d *Device) SenseAll() (T float32, RH float32, err error) {
	err = d.Read()
	if err != nil {
		return 0, 0, err
	}

	return d.tC(), d.rh(), nil
}

// SenseRH measures and returns the relative humidity [0-100].
// To get the last measured value, without sensing, use LastMeasuredRH
func (d *Device) SenseRH() (float32, error) {
	err := d.Read()
	if err != nil {
		return 0, err
	}

	return d.rh(), nil
}

// SenseTemperatureC measures then returns the Temperature in Celsius.
// To get the last measured value, without sensing, use LastMeasuredT
func (d *Device) SenseTemperatureC() (float32, error) {
	err := d.Read()
	if err != nil {
		return 0, err
	}

	return d.tC(), nil
}

// You can manage verbosity of log output in the package by changing last parameter value.
var lg = logger.NewPackageLogger("aht20", // TODO: ? ew?
	logger.DebugLevel,
	// logger.InfoLevel,
)
