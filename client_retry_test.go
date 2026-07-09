package vast_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	vast "github.com/cozy-creator/vast-ai-go-sdk"
)

func TestRetryOn429HonorsRetryAfter(t *testing.T) {
	ts := newTestServer(t)
	attempts := 0
	ts.mux.HandleFunc("/api/v0/users/current/", func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(429)
			fmt.Fprint(w, `{"error": "rate_limited"}`)
			return
		}
		fmt.Fprint(w, `{"id": 1, "email": "x@y.z", "balance": 20.0, "credit": 28.5}`)
	})
	c := ts.client(t)

	start := time.Now()
	balance, err := c.Balance(context.Background())
	if err != nil {
		t.Fatalf("Balance: %v", err)
	}
	if balance != 48.5 || attempts != 2 { // 20.0 balance + 28.5 credit
		t.Errorf("balance=%v attempts=%d", balance, attempts)
	}
	if elapsed := time.Since(start); elapsed < time.Second {
		t.Errorf("Retry-After not honored: elapsed %v < 1s", elapsed)
	}
}

func TestRetryOn5xxForIdempotent(t *testing.T) {
	ts := newTestServer(t)
	attempts := 0
	ts.mux.HandleFunc("/api/v0/instances/3/", func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			w.WriteHeader(502)
			fmt.Fprint(w, `bad gateway`)
			return
		}
		fmt.Fprint(w, `{"instances": {"id": 3, "actual_status": "running"}}`)
	})
	c := ts.client(t)

	inst, err := c.GetInstance(context.Background(), 3)
	if err != nil {
		t.Fatalf("GetInstance: %v", err)
	}
	if inst.ID != 3 || attempts != 3 {
		t.Errorf("inst=%+v attempts=%d", inst, attempts)
	}
}

func TestNoRetryOn5xxForCreate(t *testing.T) {
	ts := newTestServer(t)
	attempts := 0
	ts.mux.HandleFunc("/api/v0/asks/3/", func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(500)
		fmt.Fprint(w, `{"error": "server_error"}`)
	})
	c := ts.client(t)

	_, err := c.CreateInstance(context.Background(), 3, &vast.CreateInstanceRequest{Image: "img"})
	var apiErr *vast.APIError
	if !errors.As(err, &apiErr) || !apiErr.IsServerError() {
		t.Fatalf("want 5xx APIError, got %v", err)
	}
	if attempts != 1 {
		t.Fatalf("create retried on 5xx: %d attempts (double-rent hazard)", attempts)
	}
}

func TestRetryOn429ForCreate(t *testing.T) {
	ts := newTestServer(t)
	attempts := 0
	ts.mux.HandleFunc("/api/v0/asks/3/", func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.WriteHeader(429)
			fmt.Fprint(w, `{"error": "rate_limited"}`)
			return
		}
		fmt.Fprint(w, `{"success": true, "new_contract": 44}`)
	})
	c := ts.client(t)

	resp, err := c.CreateInstance(context.Background(), 3, &vast.CreateInstanceRequest{Image: "img"})
	if err != nil {
		t.Fatalf("CreateInstance: %v", err)
	}
	if resp.InstanceID != 44 || attempts != 2 {
		t.Errorf("resp=%+v attempts=%d", resp, attempts)
	}
}

func TestRetriesExhaust(t *testing.T) {
	ts := newTestServer(t)
	attempts := 0
	ts.mux.HandleFunc("/api/v0/users/current/", func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(503)
	})
	c := ts.client(t, vast.WithMaxRetryAttempts(2))

	_, err := c.Balance(context.Background())
	if err == nil {
		t.Fatal("want error after exhausting retries")
	}
	if attempts != 3 { // initial + 2 retries
		t.Errorf("attempts = %d, want 3", attempts)
	}
}

func TestAuthHeaderSent(t *testing.T) {
	ts := newTestServer(t)
	var auth string
	ts.mux.HandleFunc("/api/v0/users/current/", func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		fmt.Fprint(w, `{"id": 1, "balance": 0}`)
	})
	c := ts.client(t)

	if _, err := c.GetCurrentUser(context.Background()); err != nil {
		t.Fatalf("GetCurrentUser: %v", err)
	}
	if auth != "Bearer test-key" {
		t.Errorf("Authorization = %q", auth)
	}
}

func TestNewClientRequiresKey(t *testing.T) {
	if _, err := vast.NewClient(" "); err == nil {
		t.Fatal("want error for empty API key")
	}
}
