package vast_test

import (
	"context"
	"encoding/json"
	vast "github.com/cozy-creator/vast-ai-go-sdk"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLabelInstanceOnlyChangesLabel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PUT" || r.URL.Path != "/api/v0/instances/123/" {
			t.Errorf("request: %s %s", r.Method, r.URL)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if len(body) != 1 || body["label"] != "fidika/farnese" {
			t.Errorf("request changed container fields: %#v", body)
		}
		w.Write([]byte(`{"success":true}`))
	}))
	defer server.Close()
	client, err := vast.NewClient("test-key", vast.WithBaseURL(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	if err := client.LabelInstance(context.Background(), 123, "fidika/farnese"); err != nil {
		t.Fatal(err)
	}
	if err := client.LabelInstance(context.Background(), 0, "fidika/farnese"); err == nil {
		t.Fatal("empty instance ID accepted")
	}
	if err := client.LabelInstance(context.Background(), 123, "  "); err == nil {
		t.Fatal("empty label accepted")
	}
}

func TestLabelInstanceRefusal(t *testing.T) {
	ts := newTestServer(t)
	ts.handleJSON("/api/v0/instances/123/", 200, `{"success":false,"msg":"not owned"}`)
	if err := ts.client(t).LabelInstance(context.Background(), 123, "fidika/farnese"); err == nil {
		t.Fatal("provider refusal accepted")
	}
}
