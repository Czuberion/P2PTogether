// peer_service/internal/media/encode_runner.go
package media

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"sync"
	"time"
	// "github.com/u2takey/ffmpeg-go" // No longer directly used here
)

type EncoderRunner struct {
	HlsPort uint32 // Port for the HLS ingest endpoint

	mu             sync.Mutex // Protects cmd, isRunning, currentPath, currentSeqBase
	cmd            *exec.Cmd
	isRunning      bool
	currentPath    string
	currentSeqBase uint32 // Store the seqBase it was started with
}

func NewEncoderRunner(hlsPort uint32) *EncoderRunner {
	return &EncoderRunner{HlsPort: hlsPort}
}

// Start builds & starts the ffmpeg pipeline using configurations from encoder.go.
// It is a blocking call that includes retry logic.
// It runs until the context is cancelled.
func (r *EncoderRunner) Start(ctx context.Context, filePath string, seqBase uint32) error {
	r.mu.Lock()
	if r.isRunning && r.currentPath == filePath && r.currentSeqBase == seqBase {
		r.mu.Unlock()
		return nil
	}
	r.currentPath = filePath
	r.currentSeqBase = seqBase
	r.isRunning = true
	r.mu.Unlock()

	defer func() {
		r.mu.Lock()
		r.isRunning = false
		r.cmd = nil
		r.mu.Unlock()
		log.Printf("EncoderRunner: Stopped for %s (seqBase %d)", filePath, seqBase)
	}()

	log.Printf("EncoderRunner: Starting for %s, seqBase %d", filePath, seqBase)

	for {
		select {
		case <-ctx.Done():
			log.Printf("EncoderRunner: Context cancelled for %s. Stopping.", filePath)
			r.mu.Lock()
			cmdToKill := r.cmd
			r.mu.Unlock()
			if cmdToKill != nil && cmdToKill.Process != nil {
				_ = cmdToKill.Process.Kill()
				_, _ = cmdToKill.Process.Wait()
			}
			return ctx.Err()
		default:
		}

		outputURLPattern := fmt.Sprintf("http://127.0.0.1:%d/ingest/%%d.ts", r.HlsPort)
		encCfg := DefaultConfig(filePath)

		ffmpegStream := BuildHLSStreamForHTTPOutput(encCfg, outputURLPattern, seqBase)

		// Compile returns *exec.Cmd directly
		compiledCmd := ffmpegStream.Compile()
		// No error check needed here as Compile() doesn't return one.
		// If compilation fails internally, ffmpeg-go might panic or log,
		// or compiledCmd.Path might be empty, which exec.CommandContext would catch.

		var currentCmd *exec.Cmd
		if compiledCmd.Path == "" { // Basic check for compilation failure
			log.Printf("EncoderRunner: ffmpeg compilation failed for %s (seq %d) - command path is empty. Retrying in 5s...", filePath, seqBase)
			select {
			case <-time.After(5 * time.Second):
				continue // retry loop
			case <-ctx.Done():
				log.Printf("EncoderRunner: Context cancelled during compile-retry-wait for %s. Stopping.", filePath)
				return ctx.Err()
			}
		}

		if len(compiledCmd.Args) > 1 {
			currentCmd = exec.CommandContext(ctx, compiledCmd.Path, compiledCmd.Args[1:]...)
		} else {
			currentCmd = exec.CommandContext(ctx, compiledCmd.Path)
		}

		r.mu.Lock()
		if r.currentPath != filePath || r.currentSeqBase != seqBase {
			r.mu.Unlock()
			log.Printf("EncoderRunner: Task for %s (seqBase %d) was superseded by %s (seqBase %d) before command execution. Exiting this instance.",
				filePath, seqBase, r.currentPath, r.currentSeqBase)
			return fmt.Errorf("task superseded")
		}
		r.cmd = currentCmd
		r.mu.Unlock()

		currentCmd.Stdout = log.Writer()
		currentCmd.Stderr = log.Writer()

		log.Printf("EncoderRunner: Launching ffmpeg for %s (seq %d): %s %v", filePath, seqBase, currentCmd.Path, currentCmd.Args)
		runErr := currentCmd.Run()

		r.mu.Lock()
		isActiveCmd := (r.cmd == currentCmd)
		r.mu.Unlock()

		if !isActiveCmd {
			log.Printf("EncoderRunner: Command for %s (seq %d) is no longer the active command after Run(). Exiting loop.", filePath, seqBase)
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("ffmpeg task for %s (seqBase %d) superseded by a new task", filePath, seqBase)
		}

		if ctx.Err() != nil {
			log.Printf("EncoderRunner: Context done during/after ffmpeg run for %s: %v", filePath, ctx.Err())
			return ctx.Err()
		}

		if runErr != nil {
			log.Printf("EncoderRunner: ffmpeg for %s (seq %d) exited with error: %v. Retrying in 5s...", filePath, seqBase, runErr)
			select {
			case <-time.After(5 * time.Second):
				// continue retry loop
			case <-ctx.Done():
				log.Printf("EncoderRunner: Context cancelled during retry-wait for %s. Stopping.", filePath)
				return ctx.Err()
			}
		} else {
			log.Printf("EncoderRunner: ffmpeg for %s (seq %d) completed successfully (End of file). Stopping.", filePath, seqBase)
			return nil
		}
	}
}

func (r *EncoderRunner) IsRunning() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.isRunning
}

func (r *EncoderRunner) GetCurrentFile() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.currentPath
}

func (r *EncoderRunner) GetCurrentSeqBase() uint32 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.currentSeqBase
}
