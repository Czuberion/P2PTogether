// peer_service/internal/common/time.go
package common

import (
	"time"
)

// GetCurrentHLC returns the current time in milliseconds since epoch.
// This serves as a simple HLC timestamp for now.
func GetCurrentHLC() int64 {
	return time.Now().UnixMilli()
}
