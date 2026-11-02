//go:build !linux && !windows

package metrics

import "time"

// No live metrics collector exists for this platform yet; samples carry only
// a timestamp, and the dashboard shows the metrics as unavailable.
func readPlatform() raw { return raw{at: time.Now()} }
