// internal/encoder/encoder.go
package encoder

import (
	"fmt"
	"math"

	ffmpeg "github.com/u2takey/ffmpeg-go"
)

// Config gathers every tuning knob that influences live–stream latency or
// segment alignment.  Most callers will stick to the defaults returned by
// DefaultConfig(), but CLI flags / config-file values can override them.
type Config struct {
	Input          string // Input source path or "pipe:0" for stdin.
	RealTime       bool   // If true, prepend -re to pace input in real time.
	SegmentSeconds int    // HLS segment duration. Key-frames align to this. [s]
	VideoPreset    string // x264 speed/quality preset.
	Tune           string // x264 tuning profile.
	AudioBitrate   string // AAC audio bitrate.
	GOPCeiling     int    // Hard upper bound for key-frame interval. [frames]
}

// DefaultConfig returns sensible low-latency defaults.
// 2s segments, veryfast preset, zerolatency tune, 128k audio, 240 frames GOP.
func DefaultConfig(input string) Config {
	return Config{
		Input: input,

		// Keeps encoder in sync with wall-clock, avoids initial burst that
		// could overflow buffers.
		RealTime: true,

		// 2-second chunks keep ring-buffer memory small and latency down
		// while still giving mpv enough data to pre-buffer.
		SegmentSeconds: 2,

		// "veryfast" encodes ~2x real-time on a modern laptop CPU and is
		// visually acceptable for 720 p.
		VideoPreset: "veryfast",

		// Disables B-frames & look-ahead, shaving almost an entire GOP off
		// end-to-end latency.
		Tune: "zerolatency",

		// 128 k stereo AAC ~ 16 KB/s, good enough for speech and music
		AudioBitrate: "128k",

		// Safety ceiling: 2 s worth of frames *assuming* <=120 fps input.
		// The real I-frame cadence is enforced by -force_key_frames.
		GOPCeiling: 240,
	}
}

// Build assembles an ffmpeg-go stream according to the given config.
func Build(cfg Config) *ffmpeg.Stream {
	// Align I-frames by *time* rather than frame-count so any FPS (fixed or
	// VFR) can be handled.
	forceExpr := fmt.Sprintf("expr:gte(t,n_forced*%d)", cfg.SegmentSeconds)

	// Derive a ceiling GOP if caller didn’t set one. FPS isn’t known pre-probe,
	// so choose a safe >120 fps limit.
	gop := cfg.GOPCeiling
	if gop <= 0 {
		gop = int(math.Round(120 * float64(cfg.SegmentSeconds)))
	}

	inArgs := ffmpeg.KwArgs{}
	if cfg.RealTime {
		inArgs["re"] = "" // -re has no value; presence of the key is enough
	}

	return ffmpeg.Input(cfg.Input, inArgs).
		Output("pipe:1",
			// container
			ffmpeg.KwArgs{"f": "mpegts"},

			// video
			ffmpeg.KwArgs{
				"c:v":              "libx264",
				"preset":           cfg.VideoPreset,
				"tune":             cfg.Tune,
				"force_key_frames": forceExpr, // hard boundary every SegmentSeconds
				"g":                gop,       // safety net: never exceed GOP
				"keyint_min":       gop,       // …and forbid early scene-cut I-frames
				"sc_threshold":     0,         // disable scene-cut detection entirely
			},

			// audio
			ffmpeg.KwArgs{
				"c:a": "aac",
				"b:a": cfg.AudioBitrate,
			},
		)
}
