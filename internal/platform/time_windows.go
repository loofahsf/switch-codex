package platform

import (
	"golang.org/x/sys/windows"
	"time"
)

func LocalNow() time.Time {
	var info windows.Timezoneinformation
	state, err := windows.GetTimeZoneInformation(&info)
	if err != nil {
		return time.Now()
	}
	bias := info.Bias + info.StandardBias
	name := windows.UTF16ToString(info.StandardName[:])
	if state == windows.TIME_ZONE_ID_DAYLIGHT {
		bias = info.Bias + info.DaylightBias
		name = windows.UTF16ToString(info.DaylightName[:])
	}
	return time.Now().In(time.FixedZone(name, -int(bias)*60))
}
