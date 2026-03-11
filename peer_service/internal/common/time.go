// peer_service/internal/common/time.go
package common

import (
	"sync"
	"time"
)

var (
	hlcMu      sync.Mutex
	hlcTime    int64
	hlcLogical int64
)

// PackHLC packs a physical timestamp (milliseconds) and a logical counter into an int64.
// It uses 48 bits for physical time and 16 bits for the logical counter.
func PackHLC(physical, logical int64) int64 {
	return (physical << 16) | (logical & 0xFFFF)
}

// UnpackHLC extracts the physical timestamp and logical counter from an int64 HLC.
func UnpackHLC(hlc int64) (physical, logical int64) {
	physical = hlc >> 16
	logical = hlc & 0xFFFF
	return
}

// GetCurrentHLC returns the current HLC timestamp packed as an int64.
// This should be called when creating a local event.
func GetCurrentHLC() int64 {
	hlcMu.Lock()
	defer hlcMu.Unlock()

	now := time.Now().UnixMilli()
	if now > hlcTime {
		hlcTime = now
		hlcLogical = 0
	} else {
		hlcLogical++
	}

	return PackHLC(hlcTime, hlcLogical)
}

// UpdateHLC updates the local HLC based on a received remote HLC timestamp.
// It returns the updated local HLC packed as an int64.
func UpdateHLC(remoteHLC int64) int64 {
	hlcMu.Lock()
	defer hlcMu.Unlock()

	now := time.Now().UnixMilli()
	remoteTime, remoteLogical := UnpackHLC(remoteHLC)

	oldTime := hlcTime

	maxTime := now
	if remoteTime > maxTime {
		maxTime = remoteTime
	}
	if oldTime > maxTime {
		maxTime = oldTime
	}
	hlcTime = maxTime

	if hlcTime == oldTime && hlcTime == remoteTime {
		if remoteLogical > hlcLogical {
			hlcLogical = remoteLogical + 1
		} else {
			hlcLogical = hlcLogical + 1
		}
	} else if hlcTime == oldTime {
		hlcLogical++
	} else if hlcTime == remoteTime {
		hlcLogical = remoteLogical + 1
	} else {
		hlcLogical = 0
	}

	return PackHLC(hlcTime, hlcLogical)
}
