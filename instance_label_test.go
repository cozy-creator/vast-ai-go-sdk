package vast_test

import (
	"context"
	"testing"
)

func TestLabelInstanceOnlyChangesLabel(t *testing.T) {
	ts := newTestServer(t)
	ts.handleJSON("/api/v0/instances/123/", 200, `{"success":true}`)
	client := ts.client(t)
	if err := client.LabelInstance(context.Background(), 123, "fidika/farnese"); err != nil {
		t.Fatal(err)
	}
	if ts.lastMethod != "PUT" || len(ts.lastBody) != 1 || ts.lastBody["label"] != "fidika/farnese" {
		t.Fatalf("request: %s %#v", ts.lastMethod, ts.lastBody)
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
