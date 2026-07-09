package vast_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	vast "github.com/cozy-creator/vast-ai-go-sdk"
)

// testServer wraps httptest with per-route handlers and request capture.
type testServer struct {
	*httptest.Server
	mux *http.ServeMux
	// lastBody holds the most recent decoded JSON request body.
	lastBody map[string]interface{}
}

func newTestServer(t *testing.T) *testServer {
	t.Helper()
	mux := http.NewServeMux()
	ts := &testServer{mux: mux, Server: httptest.NewServer(mux)}
	t.Cleanup(ts.Close)
	return ts
}

// handleJSON registers a handler that captures the request body and writes
// status + a JSON payload (string or any marshallable value).
func (ts *testServer) handleJSON(pattern string, status int, payload interface{}) {
	ts.mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		ts.lastBody = body
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		switch p := payload.(type) {
		case string:
			_, _ = w.Write([]byte(p))
		case []byte:
			_, _ = w.Write(p)
		default:
			_ = json.NewEncoder(w).Encode(p)
		}
	})
}

func (ts *testServer) client(t *testing.T, opts ...vast.ClientOption) *vast.Client {
	t.Helper()
	opts = append([]vast.ClientOption{
		vast.WithBaseURL(ts.URL),
		vast.WithRetryDelay(time.Millisecond),
	}, opts...)
	c, err := vast.NewClient("test-key", opts...)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return b
}
