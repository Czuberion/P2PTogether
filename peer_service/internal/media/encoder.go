// internal/media/encoder.go
package media

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"time"

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
	HlsIngestURL   string // Base URL for HLS segment ingest (e.g., "http://127.0.0.1:PORT")
	PlaylistPath   string // Full path where ffmpeg should write its M3U8 playlist file
}

// DefaultConfig returns sensible low-latency defaults.
// 2s segments, veryfast preset, zerolatency tune, 128k audio, 240 frames GOP.
func DefaultConfig(input string, hlsIngestURL string, playlistPath string) Config {
	return Config{
		Input: input,

		// Keeps encoder in sync with wall-clock, avoids initial burst that
		// could overflow buffers.
		// NOTE: Keep -re disabled for now to avoid real-time throttling when
		// benchmarking file inputs. Re-enable for live/pipe inputs when we want
		// wall-clock pacing again.
		RealTime: false,

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

		HlsIngestURL: hlsIngestURL,

		PlaylistPath: playlistPath,
	}
}

// GenerateTempPlaylistPath creates a unique temporary path for the HLS playlist.
func (c *Config) GenerateTempPlaylistPath(seqBase uint32) string {
	// Use a unique temp file per stream start
	return filepath.Join(os.TempDir(), fmt.Sprintf("p2ptogether-master-%d-%d.m3u8", seqBase, time.Now().UnixNano()))
}

// BuildHLSStreamForHTTPOutput assembles an ffmpeg-go stream configured to output HLS segments
// via HTTP PUT to the specified HlsIngestURL, starting with seqBase.
// It uses the provided Config for encoding parameters.
func BuildHLSStreamForHTTPOutput(cfg Config, seqBase uint32) *ffmpeg.Stream {
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

	inputStream := ffmpeg.Input(cfg.Input, inArgs)

	// Segment filename pattern for HTTP PUT. FFmpeg will combine this with HlsIngestURL.
	// Example: if HlsIngestURL = "http://127.0.0.1:8080", segments will be PUT to
	// "http://127.0.0.1:8080/ingest/%06d.ts"
	segmentFilenamePattern := fmt.Sprintf("%s/ingest/%%06d.ts", cfg.HlsIngestURL)

	// These are the output arguments specific to HLS PUT
	outputArgs := ffmpeg.KwArgs{
		// HLS Muxer options
		"f":                    "hls",
		"hls_time":             fmt.Sprintf("%d", cfg.SegmentSeconds),
		"hls_list_size":        "0",                                 // Keep all segments for EVENT playlist type
		"hls_flags":            "independent_segments+omit_endlist", // Essential for clean segment boundaries, omit_endlist for live
		"start_number":         fmt.Sprintf("%d", seqBase),          // Starting HLS media sequence number
		"hls_segment_filename": segmentFilenamePattern,              // Specifies the URL for PUT-ing segments
		"method":               "PUT",                               // HTTP PUT for segments
		// The actual URL pattern is the first argument to Output(), not a KwArg here.

		// Video encoding options
		"c:v":              "libx264",
		"preset":           cfg.VideoPreset,
		"tune":             cfg.Tune,
		"force_key_frames": forceExpr,
		"g":                fmt.Sprintf("%d", gop),
		"keyint_min":       fmt.Sprintf("%d", gop), // Ensure keyint_min matches gop for zerolatency compatibility
		"sc_threshold":     "0",

		// Audio encoding options
		"c:a": "aac",
		"b:a": cfg.AudioBitrate,
	}

	// The first argument to Output() is the playlist file path.
	// This is the path the M3U8Monitor will watch.
	return inputStream.Output(cfg.PlaylistPath, outputArgs).GlobalArgs("-hide_banner")
}
