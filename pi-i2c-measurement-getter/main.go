package pi_i2c_measurement_getter

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// TODO: save time as hh:mm:ss

type dataPt struct {
	time                    time.Time
	tempF, RelativeHumidity float64
	lux                     uint16
}

func (pt *dataPt) UnmarshalJSON(bs []byte) error {
	inp := string(bs)
	parts := strings.Split(inp, ",")
	if len(parts) != 4 {
		return errors.New("invalid format")
	}
	var err error
	out := dataPt{}
	out.time, err = time.Parse(time.DateTime, parts[0][1:])
	if err != nil {
		return err
	}
	out.tempF, err = strconv.ParseFloat(parts[1], 64)
	if err != nil {
		return err
	}
	out.RelativeHumidity, err = strconv.ParseFloat(parts[2], 64)
	if err != nil {
		return err
	}
	out.lux, err = strToUint16(parts[3][:len(parts[3])])
	if err != nil {
		return err
	}
	*pt = out
}

func (pt dataPt) MarshalJSON() ([]byte, error) {
	return []byte(pt.String()), nil
}

func uint16ToStr(u uint16) string {
	// TODO: this
}
func strToUint16(s string) (uint16, error) {
	// TODO: this
}

func (pt dataPt) String() string {
	return fmt.Sprintf(`[%s,%.1f,%.1f,%s]`, pt.time.Format(time.DateTime), pt.tempF, pt.RelativeHumidity, uint16ToStr(pt.lux))
}

func main() {
	// TODO: load in any measurement files from an erroneous last run
	thisTime := time.Now()
	// TODO: check for old files

	newMeasurement := make(chan string)
	flushFiles := time.NewTicker(5 * time.Minute)
	go func() {
		for {
			meas := ""
			select {
			case <-flushFiles.C:
				break
			case meas = <-newMeasurement:
				// TODO:  try to send out measurement
				break
			}
			// Check for day files, try to send them
			// TODO: if there are any files to flush, flush them
		}
	}()
	// TODO: start storing unsent files

}

func main() {
	nodeName := "NameFixMe!"     // TODO: this
	lastTime := time.Now()       // TODO: fix
	thisQuarterStart := lastTime // TODO: fix
	thisWeekStart := lastTime    // TODO: fix
	thisDayStart := lastTime     // TODO: fix
	// Hourly after 3mo (min/max/avg)
	// 5min increments from 1wk to 3mo (min/max/avg)
	// 30 second increments for now until 1wk
	//
	// TODO: create minute-ly?
	thisDayData := make([]dataPt, 0, 24*60*2)
	for {
		thisTime := time.Now()
		var tempF, RelHumidity, lux = 0.0, 0.0, uint16(0) // TODO: measure all

		pt := dataPt{
			time:             thisTime,
			tempF:            tempF,
			RelativeHumidity: RelHumidity,
			lux:              lux,
		}
		if lastTime.Day() != thisTime.Day() { // IS NEXT DAY
			// TODO: if day has not been sent, store day file
			// Check week
			//// TODO: add last day to week file
			//if thisTime.Weekday() == 0 {
			//	// TODO: save week file and begin new one
			//
			//}
			//// TODO: check month
			//if lastTime.Month() != thisTime.Month() {
			//	// Check quarter
			//	if (thisTime.Month()-1)%3 == 0 { // TODO: is this ok?
			//
			//	}
			//}
			//
			//if lastTime.Year() != thisTime.Year() {
			//	// TODO: start new year
			//}
			//
			//// TODO: add last day to quarter file (if quarter is not new)
			//// TODO: start next day file
			//// Parse last day file and insert into week file
			//// TODO: if week file is full, finish it and start a new week file
			//// TODO: if quarter file is full, finish it and start a new quarter file
		}
		// Add this day's data to current day file
		thisDayData = append(thisDayData, pt)
		// TODO: send this time's data to current day file
		time.Sleep(30 * time.Second)
	}
	// Every 15 seconds, measure
}
