//go:build unix

package platform

import (
	"os"
	"time"
)

// Go caches time.Local. Reload the system zone so an OS timezone change takes
// effect without restarting the app, as it did in the Rust implementation.
func LocalNow() time.Time {
	now := time.Now()
	if zone := os.Getenv("TZ"); zone != "" {
		if loc, err := time.LoadLocation(zone); err == nil {
			return now.In(loc)
		}
	}
	if data, err := os.ReadFile("/etc/localtime"); err == nil {
		if loc, err := time.LoadLocationFromTZData("Local", data); err == nil {
			return now.In(loc)
		}
	}
	return now
}
