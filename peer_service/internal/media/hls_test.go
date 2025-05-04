package media_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"peer_service/internal/media"
)

func TestEmptyPlaylistReturns200(t *testing.T) {
	rb := media.NewRingBuffer(6) // 3 slots, initially empty
	plH, _ := media.Handler(rb)

	req := httptest.NewRequest(http.MethodGet, "/stream.m3u8", nil)
	w := httptest.NewRecorder()
	plH(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}
	body := w.Body.String()
	if !strings.HasPrefix(body, "#EXTM3U") {
		t.Fatalf("playlist missing #EXTM3U header:\n%s", body)
	}
	if strings.Contains(body, "#EXTINF") {
		t.Fatalf("no segments expected, but got one:\n%s", body)
	}
}
