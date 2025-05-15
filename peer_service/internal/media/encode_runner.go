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
// It runs until the context is cancelled or ffmpeg finishes successfully (EOF).
// Returns nil on successful EOF, context.Canceled if cancelled, or other error from ffmpeg.
func (r *EncoderRunner) Start(ctx context.Context, filePath string, seqBase uint32) error {
	r.mu.Lock()
	if r.isRunning && r.currentPath == filePath && r.currentSeqBase == seqBase {
		log.Printf("EncoderRunner: Start called for already running task %s (seqBase %d). No action.", filePath, seqBase)
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
		// r.cmd = nil // Keep cmd for potential inspection if needed, or clear it.
		r.mu.Unlock()
		log.Printf("EncoderRunner: Goroutine for %s (seqBase %d) finished.", filePath, seqBase)
	}()

	log.Printf("EncoderRunner: Starting ffmpeg process for %s, seqBase %d", filePath, seqBase)

	for {
		select {
		case <-ctx.Done():
			log.Printf("EncoderRunner: Context cancelled for %s before ffmpeg start. Stopping.", filePath)
			r.mu.Lock()
			cmdToKill := r.cmd
			r.mu.Unlock() // Unlock before potentially blocking kill/wait
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

		// Use exec.CommandContext to ensure ffmpeg is killed if the context is cancelled.

		if len(compiledCmd.Args) > 1 {
			currentCmd = exec.CommandContext(ctx, compiledCmd.Path, compiledCmd.Args[1:]...)
		} else {
			currentCmd = exec.CommandContext(ctx, compiledCmd.Path)
		}
		currentCmd.Stdout = log.Writer() // Capture ffmpeg stdout
		currentCmd.Stderr = log.Writer() // Capture ffmpeg stderr

		r.mu.Lock()
		if r.currentPath != filePath || r.currentSeqBase != seqBase {
			r.mu.Unlock()
			log.Printf("EncoderRunner: Task for %s (seqBase %d) was superseded by %s (seqBase %d) before command execution. Exiting this instance.",
				filePath, seqBase, r.currentPath, r.currentSeqBase)
			return fmt.Errorf("task superseded before execution")
		}
		r.cmd = currentCmd
		r.mu.Unlock()

		log.Printf("EncoderRunner: Launching ffmpeg for %s (seq %d): %s %v", filePath, seqBase, currentCmd.Path, currentCmd.Args)
		runErr := currentCmd.Run() // This blocks until ffmpeg exits or context is cancelled

		r.mu.Lock()
		isActiveCmd := (r.cmd == currentCmd)
		r.mu.Unlock()

		if !isActiveCmd {
			log.Printf("EncoderRunner: Command for %s (seq %d) is no longer the active command after Run(). Exiting loop.", filePath, seqBase)
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("ffmpeg task for %s (seqBase %d) superseded by a new task during execution", filePath, seqBase)
		}

		// At this point, currentCmd has finished.
		// Check context error first (e.g. if it was cancelled while ffmpeg was running)

		if ctx.Err() != nil {
			log.Printf("EncoderRunner: Context done for %s (seq %d) after ffmpeg run: %v", filePath, seqBase, ctx.Err())
			return ctx.Err()
		}

		// If context is not done, check runErr
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
			// ffmpeg completed successfully (runErr is nil) and context was not cancelled. This is EOF.
			log.Printf("EncoderRunner: ffmpeg for %s (seq %d) completed successfully (End of File).", filePath, seqBase)
			return nil // Signal successful completion (EOF)
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
