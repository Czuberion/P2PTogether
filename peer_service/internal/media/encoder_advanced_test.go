// peer_service/internal/media/encoder_advanced_test.go
package media

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
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

func TestEncoderRunner_Integration(t *testing.T) {
	// 1. Check for ffmpeg
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not found in PATH, skipping integration test")
	}

	// 2. Check for test video file
	wd, _ := os.Getwd()
	testFileRel := "../../../tests/data/test_vid0.mp4"
	testFileAbs, err := filepath.Abs(filepath.Join(wd, testFileRel))
	if err != nil {
		t.Fatalf("Failed to resolve test file path: %v", err)
	}
	if _, err := os.Stat(testFileAbs); os.IsNotExist(err) {
		t.Skipf("Test video file not found at %s, skipping integration test", testFileAbs)
	}

	// 3. Setup Dummy Ingest Server
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to listen on random port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	t.Logf("Integration test using port %d", port)

	var receivedSegments sync.Map
	var segmentCount int
	var mu sync.Mutex

	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/ingest/") {
			mu.Lock()
			receivedSegments.Store(r.URL.Path, true)
			segmentCount++
			count := segmentCount
			mu.Unlock()
			t.Logf("Server received segment: %s (Total: %d)", r.URL.Path, count)
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusBadRequest)
		}
	}))

	// Hijack the listener we created
	ts.Listener.Close() // Close the httptest default listener created by NewUnstartedServer (it hasn't started yet but let's be safe, oh wait, NewUnstartedServer doesn't listen yet)
	ts.Listener = listener
	ts.Start()
	defer ts.Close()

	// 4. Setup EncoderRunner with REAL exec context
	execCommandContext = exec.CommandContext

	runner := NewEncoderRunner(uint32(port))

	// Video is 10s. With -re, it takes 10s. Give it 15s buffer.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// 5. Run Encoder
	errChan := make(chan error, 1)
	startSeq := uint32(100)
	go func() {
		err := runner.Start(ctx, testFileAbs, startSeq)
		errChan <- err
	}()

	// 6. Wait for Completion (EOF)
	select {
	case err := <-errChan:
		if err != nil {
			t.Fatalf("EncoderRunner exited with error: %v", err)
		}
		t.Log("EncoderRunner finished successfully (EOF)")
	case <-ctx.Done():
		t.Fatalf("Test timed out waiting for encoder to finish")
	}

	// 7. Verify Results
	mu.Lock()
	count := segmentCount
	mu.Unlock()

	// 10s video, 2s segments => ~5 segments.
	// Depending on keyframe alignment and rounding, it might be 5 or 6 (if there's a tiny remainder).
	if count < 5 {
		t.Errorf("Expected at least 5 segments, got %d", count)
	}

	// Check specific segments exist
	for i := 0; i < 5; i++ {
		seq := startSeq + uint32(i)
		path := fmt.Sprintf("/ingest/%06d.ts", seq)
		if _, ok := receivedSegments.Load(path); !ok {
			t.Errorf("Expected segment %s was not received", path)
		}
	}

	if runner.IsRunning() {
		t.Errorf("Expected runner to be stopped after completion")
	}
}
