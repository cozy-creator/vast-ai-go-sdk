package vast_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	vast "github.com/cozy-creator/vast-ai-go-sdk"
)

func TestCreateInstanceBodyAndResponse(t *testing.T) {
	ts := newTestServer(t)
	ts.handleJSON("/api/v0/asks/19882713/", 200, `{"success": true, "new_contract": 12345678}`)
	c := ts.client(t)

	resp, err := c.CreateInstance(context.Background(), 19882713, &vast.CreateInstanceRequest{
		Image:   "ghcr.io/cozy-creator/cell-producer:0.3.1",
		Env:     map[string]string{"SESSION_SPEC_URL": "https://r2/presigned"},
		Ports:   []string{"8080:8080"},
		Onstart: "curl -fsSL $SESSION_SPEC_URL | sh",
		DiskGB:  32,
		Label:   "forge-session-42",
	})
	if err != nil {
		t.Fatalf("CreateInstance: %v", err)
	}
	if resp.InstanceID != 12345678 {
		t.Errorf("InstanceID = %d, want 12345678", resp.InstanceID)
	}

	body := ts.lastBody
	if body["client_id"] != "me" || body["image"] != "ghcr.io/cozy-creator/cell-producer:0.3.1" {
		t.Errorf("identity fields: %v", body)
	}
	env := body["env"].(map[string]interface{})
	if env["SESSION_SPEC_URL"] != "https://r2/presigned" || env["-p 8080:8080"] != "1" {
		t.Errorf("env encoding: %v", env)
	}
	if body["runtype"] != vast.RunTypeSSH {
		t.Errorf("runtype = %v, want ssh (onstart set)", body["runtype"])
	}
	if body["onstart"] != "curl -fsSL $SESSION_SPEC_URL | sh" {
		t.Errorf("onstart = %v", body["onstart"])
	}
	if body["disk"] != float64(32) || body["label"] != "forge-session-42" {
		t.Errorf("disk/label: %v", body)
	}
	if body["target_state"] != "running" {
		t.Errorf("target_state = %v", body["target_state"])
	}
	if _, hasPrice := body["price"]; hasPrice {
		t.Errorf("price must be absent for on-demand")
	}
}

func TestCreateInstanceRunTypeDefaults(t *testing.T) {
	ts := newTestServer(t)
	ts.handleJSON("/api/v0/asks/1/", 200, `{"success": true, "new_contract": 2}`)
	c := ts.client(t)

	// No onstart -> headless args mode (image entrypoint runs).
	if _, err := c.CreateInstance(context.Background(), 1, &vast.CreateInstanceRequest{Image: "img"}); err != nil {
		t.Fatalf("CreateInstance: %v", err)
	}
	if ts.lastBody["runtype"] != vast.RunTypeArgs {
		t.Errorf("runtype = %v, want args", ts.lastBody["runtype"])
	}
}

func TestCreateInstanceErrorTaxonomy(t *testing.T) {
	cases := []struct {
		name     string
		status   int
		body     string
		sentinel error
	}{
		{"offer 404", 404, `{"success": false, "error": "no_such_ask", "msg": "ask not found"}`, vast.ErrOfferGone},
		{"offer 410 gone", 410, `{"success": false, "error": "unavailable", "msg": "instance no longer available"}`, vast.ErrOfferGone},
		{"insufficient credit", 400, `{"success": false, "error": "insufficient_credit", "msg": "please add credit"}`, vast.ErrInsufficientCredit},
		{"unauthorized", 401, `{"success": false, "error": "invalid_apikey", "msg": "bad key"}`, vast.ErrUnauthorized},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts := newTestServer(t)
			ts.handleJSON("/api/v0/asks/7/", tc.status, tc.body)
			c := ts.client(t)

			_, err := c.CreateInstance(context.Background(), 7, &vast.CreateInstanceRequest{Image: "img"})
			if !errors.Is(err, tc.sentinel) {
				t.Fatalf("err = %v, want errors.Is(%v)", err, tc.sentinel)
			}
		})
	}
}

func TestCreateInstanceSuccessFalseIn200(t *testing.T) {
	ts := newTestServer(t)
	ts.handleJSON("/api/v0/asks/7/", 200, `{"success": false, "error": "invalid_args", "msg": "bad disk"}`)
	c := ts.client(t)

	_, err := c.CreateInstance(context.Background(), 7, &vast.CreateInstanceRequest{Image: "img"})
	var apiErr *vast.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "invalid_args" {
		t.Fatalf("want APIError{Code: invalid_args}, got %v", err)
	}
}

func TestCreateInstanceValidation(t *testing.T) {
	ts := newTestServer(t)
	c := ts.client(t)
	var verr *vast.ValidationError

	if _, err := c.CreateInstance(context.Background(), 0, &vast.CreateInstanceRequest{Image: "img"}); !errors.As(err, &verr) {
		t.Errorf("offerID=0: want ValidationError, got %v", err)
	}
	if _, err := c.CreateInstance(context.Background(), 1, &vast.CreateInstanceRequest{}); !errors.As(err, &verr) {
		t.Errorf("empty image: want ValidationError, got %v", err)
	}
	long := make([]byte, 4049)
	for i := range long {
		long[i] = 'x'
	}
	if _, err := c.CreateInstance(context.Background(), 1, &vast.CreateInstanceRequest{Image: "img", Onstart: string(long)}); !errors.As(err, &verr) {
		t.Errorf("long onstart: want ValidationError, got %v", err)
	}
}

func TestGetInstanceEnvelope(t *testing.T) {
	ts := newTestServer(t)
	ts.handleJSON("/api/v0/instances/12345678/", 200, fixture(t, "instance.json"))
	c := ts.client(t)

	inst, err := c.GetInstance(context.Background(), 12345678)
	if err != nil {
		t.Fatalf("GetInstance: %v", err)
	}
	if inst.ID != 12345678 || inst.Label != "forge-session-42" {
		t.Errorf("identity: %+v", inst)
	}
	if !inst.IsRunning() || inst.IsTerminal() {
		t.Errorf("state helpers: actual=%q", inst.ActualStatus)
	}
	if inst.SSHHost != "ssh4.vast.ai" || inst.SSHPort != 21506 {
		t.Errorf("ssh fields: %+v", inst)
	}
	if inst.DPHTotal != 0.089 {
		t.Errorf("DPHTotal = %v", inst.DPHTotal)
	}
	if got := inst.StartedAt().Year(); got != 2026 {
		t.Errorf("StartedAt year = %d, want 2026", got)
	}
}

func TestListInstancesPaginates(t *testing.T) {
	ts := newTestServer(t)
	page := 0
	ts.mux.HandleFunc("/api/v1/instances/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("limit") != "25" {
			t.Errorf("limit = %q, want 25", r.URL.Query().Get("limit"))
		}
		page++
		switch page {
		case 1:
			if tok := r.URL.Query().Get("after_token"); tok != "" {
				t.Errorf("page 1 after_token = %q, want empty", tok)
			}
			fmt.Fprint(w, `{"success": true, "instances": [{"id": 1, "actual_status": "running"}], "next_token": "tok-2"}`)
		case 2:
			if tok := r.URL.Query().Get("after_token"); tok != "tok-2" {
				t.Errorf("page 2 after_token = %q, want tok-2", tok)
			}
			fmt.Fprint(w, `{"success": true, "instances": [{"id": 2, "actual_status": "exited"}], "next_token": null}`)
		default:
			t.Errorf("unexpected page %d", page)
			fmt.Fprint(w, `{"success": true, "instances": []}`)
		}
	})
	c := ts.client(t)

	instances, err := c.ListInstances(context.Background())
	if err != nil {
		t.Fatalf("ListInstances: %v", err)
	}
	if len(instances) != 2 || instances[0].ID != 1 || instances[1].ID != 2 {
		t.Fatalf("instances = %+v", instances)
	}
	if !instances[1].IsTerminal() {
		t.Errorf("exited must be terminal")
	}
}

func TestDestroyInstance(t *testing.T) {
	ts := newTestServer(t)
	var method string
	ts.mux.HandleFunc("/api/v0/instances/9/", func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		fmt.Fprint(w, `{"success": true, "msg": "Instance destroyed successfully"}`)
	})
	c := ts.client(t)

	if err := c.DestroyInstance(context.Background(), 9); err != nil {
		t.Fatalf("DestroyInstance: %v", err)
	}
	if method != http.MethodDelete {
		t.Errorf("method = %q, want DELETE", method)
	}
}

func TestDestroyInstanceNotFound(t *testing.T) {
	ts := newTestServer(t)
	ts.handleJSON("/api/v0/instances/9/", 404, `{"success": false, "error": "no_such_instance"}`)
	c := ts.client(t)

	err := c.DestroyInstance(context.Background(), 9)
	if !errors.Is(err, vast.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestWaitForInstanceRunning(t *testing.T) {
	ts := newTestServer(t)
	polls := 0
	ts.mux.HandleFunc("/api/v0/instances/5/", func(w http.ResponseWriter, r *http.Request) {
		polls++
		status := "loading"
		if polls >= 3 {
			status = "running"
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"instances": map[string]interface{}{"id": 5, "actual_status": status},
		})
	})
	c := ts.client(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	inst, err := c.WaitForInstanceRunning(ctx, 5, time.Millisecond)
	if err != nil {
		t.Fatalf("WaitForInstanceRunning: %v", err)
	}
	if !inst.IsRunning() || polls < 3 {
		t.Errorf("inst=%+v polls=%d", inst, polls)
	}
}

func TestWaitForInstanceRunningTerminal(t *testing.T) {
	ts := newTestServer(t)
	ts.handleJSON("/api/v0/instances/5/", 200,
		`{"instances": {"id": 5, "actual_status": "exited", "status_msg": "docker pull failed"}}`)
	c := ts.client(t)

	inst, err := c.WaitForInstanceRunning(context.Background(), 5, time.Millisecond)
	if err == nil {
		t.Fatalf("want terminal-state error")
	}
	if inst == nil || inst.ActualStatus != vast.StatusExited {
		t.Errorf("last instance = %+v", inst)
	}
}
