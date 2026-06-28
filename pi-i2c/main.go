package main

import (
	"context"
	gy302 "github.com/d2r2/go-bh1750" //"github.com/reeceappling/i2cSensors/gy302"
	"github.com/d2r2/go-i2c"
	"github.com/d2r2/go-logger"
	shell "github.com/d2r2/go-shell"
	"github.com/reeceappling/i2cSensors/aht20"
	"math"
	"os"
	"syscall"
	"time"
)

var lg = logger.NewPackageLogger("main",
	logger.DebugLevel,
	// logger.InfoLevel,
)

const i2cBus = 1             // TODO: ensure correct
const lightAddr uint8 = 0x23 // GY-302 // when addr pin is attached to GND or floating // 0x5C when addr pin connected to VCC
const rhtAddr uint8 = 0x38   // AHT20

// How to find I2C bus allocation and device address: Use i2cdetect utility in format "i2cdetect -y X", where X may vary from 0 to 5 or more, to discover address occupied by peripheral device. To install utility you should run apt install i2c-tools on debian-kind system. i2cdetect -y 1 sample output:

func main() {}

func mainOld() {
	println("starting program")
	var err error
	defer func() {
		if err != nil {
			println("Error during shutdown", err.Error())
		}
	}()
	println("setting up rht conn")
	// Create new connection to I2C bus on line 1 with address 0x27
	connRht, err := i2c.NewI2C(rhtAddr, 1)
	if err != nil {
		panic("failed to setup i2c for AHT20 device: " + err.Error())
	}
	defer func() {
		if err = connRht.Close(); err != nil {
			println("error closing i2c for AHT20 device: " + err.Error())
		}
	}()
	println("setting up light conn")
	connLight, err := i2c.NewI2C(lightAddr, 1)
	if err != nil {
		panic("failed to setup i2c for GY302 device: " + err.Error())
	}
	defer func() {
		if err = connLight.Close(); err != nil {
			println("error closing i2c for GY302 device: " + err.Error())
		}
	}()
	println("setting up rht device")
	rhtDevice := aht20.NewFrom(connRht)
	rhtDevice.SetConfigDelay(1 * time.Second).SetWriteReadDelay(1 * time.Second)
	println("setting up light device")
	luxDevice := gy302.NewBH1750()
	println("configuring up rht device")
	if err = rhtDevice.Configure(); err != nil {
		panic("failed to configure AHT20: " + err.Error())
	}
	println("using rht device")
	TC, RH, err := rhtDevice.SenseAll()
	if err != nil {
		panic("failed to sense AHT20: " + err.Error())
	}
	// Reset sensor
	err = luxDevice.Reset(connLight)
	if err != nil {
		panic(err.Error())
	}
	// Reset sensitivity factor to default value // 255
	err = luxDevice.ChangeSensivityFactor(connLight, luxDevice.GetDefaultSensivityFactor())
	if err != nil {
		panic("sens factor change err: " + err.Error())
	}

	//err = luxDevice.ChangeSensivityFactor(200)
	//if err != nil {
	//	panic("failed to change sens factor: " + err.Error())
	//}
	err = luxDevice.Reset(connLight)
	if err != nil {
		panic(err.Error())
	}
	println("powering on light device")
	if err = luxDevice.PowerOn(connLight); err != nil {
		panic("failed to power on GY302: " + err.Error())
	}

	println("using light device")
	lux, err := luxDevice.MeasureAmbientLight(connLight, gy302.HighResolution)
	if err != nil {
		panic("failed to measure ambient light: " + err.Error())
	}
	lux2, err := luxDevice.FetchMeasuredAmbientLight(connLight)
	if err != nil {
		panic("failed to measure ambient light: " + err.Error())
	}
	luxPct := float64(lux) * 100.0 / float64(math.MaxUint16)
	luxPct2 := float64(lux2) * 100.0 / float64(math.MaxUint16)
	TF := (TC * 9.0 / 5.0) + 32.0
	println("Temp", TC, "C")
	println("Temp", TF, "F")
	println("Humidity", RH, "%")
	println("lux", lux, lux2)
	println("luxPct", luxPct, luxPct2) // TODO: broken, seeing 3.906310 a lot

	//err = luxDevice.ChangeSensivityFactor(connLight, 32)
	//if err != nil {
	//	panic("sens factor change err: " + err.Error())
	//}
	println("powering on light device")
	err = luxDevice.Reset(connLight)
	if err != nil {
		panic(err.Error())
	}
	if err = luxDevice.PowerOn(connLight); err != nil {
		panic("failed to power on GY302: " + err.Error())
	}
	resolution := gy302.LowResolution
	amb, err := luxDevice.MeasureAmbientLight(connLight, resolution)
	if err != nil {
		lg.Fatal(err)
	}
	err = luxDevice.Reset(connLight)
	if err != nil {
		panic(err.Error())
	}
	if err = luxDevice.PowerOn(connLight); err != nil {
		panic("failed to power on GY302: " + err.Error())
	}
	lg.Infof("Ambient light (%s) = %v lx", resolution, amb)
	resolution = gy302.HighResolution
	amb, err = luxDevice.MeasureAmbientLight(connLight, resolution)
	if err != nil {
		lg.Fatal(err)
	}
	err = luxDevice.Reset(connLight)
	if err != nil {
		panic(err.Error())
	}
	if err = luxDevice.PowerOn(connLight); err != nil {
		panic("failed to power on GY302: " + err.Error())
	}
	lg.Infof("Ambient light (%s) = %v lx", resolution, amb)
	resolution = gy302.HighestResolution
	amb, err = luxDevice.MeasureAmbientLight(connLight, resolution)
	if err != nil {
		lg.Fatal(err)
	}
	lg.Infof("Ambient light (%s) = %v lx", resolution, amb)
	lg.Info("continuous light measurement...")
	lg.Notify("**********************************************************************************************")
	lg.Notify("*** Measure ambient light continuously")
	lg.Notify("**********************************************************************************************")
	resolution = gy302.HighResolution
	wait, err := luxDevice.StartMeasureAmbientLightContinuously(connLight, resolution)
	if err != nil {
		lg.Fatal(err)
	}
	// create context with cancellation possibility
	ctx, cancel := context.WithCancel(context.Background())
	// use done channel as a trigger to exit from signal waiting goroutine
	done := make(chan struct{})
	defer close(done)
	// build actual signal list to control
	signals := []os.Signal{os.Kill, os.Interrupt}
	if shell.IsLinuxMacOSFreeBSD() {
		signals = append(signals, syscall.SIGTERM)
	}
	// run goroutine waiting for OS termination events, including keyboard Ctrl+C
	shell.CloseContextOnSignals(cancel, done, signals...)
	for i := 0; i < 20; i++ {
		time.Sleep(20 * time.Millisecond)
		amb, err := luxDevice.FetchMeasuredAmbientLight(connLight)
		if err != nil {
			lg.Fatal(err)
		}
		lg.Infof("Ambient light (%s) = %v lx", resolution, amb)
		select {
		// Check for termination request.
		case <-ctx.Done():
			err = luxDevice.PowerDown(connLight)
			if err != nil {
				lg.Fatal(err)
			}
			lg.Fatal(ctx.Err())

			// Wait recommended duration.
			// You can increase delay - this
			// doesn't affect to measured value.
		case <-time.After(wait):
		}
	}
	err = luxDevice.PowerDown(connLight)
	if err != nil {
		lg.Fatal(err)
	}

}
