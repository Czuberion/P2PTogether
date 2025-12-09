// peer_service/internal/media/encoder_advanced_test.go
package media

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"
)

// TestEncoderRunner_Lifecycle mocks ffmpeg execution to verify state transitions.
func TestEncoderRunner_Lifecycle(t *testing.T) {
	// 1. Setup Mock
	// When execCommandContext is called, it will run *this test binary*
	// with environment variable GO_WANT_HELPER_PROCESS=1.
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		cs := []string{"-test.run=TestHelperProcess", "--", name}
		cs = append(cs, args...)
		cmd := exec.CommandContext(ctx, os.Args[0], cs...)
		cmd.Env = []string{"GO_WANT_HELPER_PROCESS=1"}
		return cmd
	}
	// Restore after test
	defer func() { execCommandContext = exec.CommandContext }()

	// 2. Initialize Runner
	runner := NewEncoderRunner(8080)

	// 3. Start Runner in Goroutine
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Use a channel to synchronize/wait for start
	started := make(chan struct{})

	// We need a way to know it *started*. IsRunning becomes true immediately in Start().
	// But Start() blocks.

	go func() {
		close(started)
		// This will block until "ffmpeg" (our helper) finishes or context invalid
		err := runner.Start(ctx, "/tmp/test_file.mp4", 0)
		if err != nil && err != context.Canceled {
			// t.Errorf here might be racy if test function ended, but for logging it's fine-ish
			// better to log
		}
	}()

	<-started
	// Give it a tiny bit of time to lock mutex and set isRunning=true
	time.Sleep(50 * time.Millisecond)

	// 4. Verify Running State
	if !runner.IsRunning() {
		t.Error("Expected runner to be running after Start()")
	}

	// 5. Verify Context Cancellation
	cancel() // Cancel the context
	// Runner should exit.

	// Wait a bit for cleanup
	time.Sleep(100 * time.Millisecond)

	if runner.IsRunning() {
		t.Error("Expected runner to NOT be running after cancellation")
	}
}

// TestHelperProcess is the mock "ffmpeg" process.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	// This is the "ffmpeg" process running.
	// Simulate work by sleeping.
	// We sleep long enough so the main test can assert IsRunning=true.
	// The main test will cancel the context, which kills this process.
	time.Sleep(10 * time.Second)
	os.Exit(0)
}
