package httpx

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequestLoggerPreservesEventStreamFlushing(t *testing.T) {
	rec := httptest.NewRecorder()
	handler := RequestLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("logging middleware removed streaming support")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: connection_status\ndata: {\"status\":\"online\"}\n\n")
		flusher.Flush()
	}))
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/api/v3/events/stream", nil))
	if !rec.Flushed || rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("stream not flushed correctly: flushed=%v code=%d", rec.Flushed, rec.Code)
	}
}
