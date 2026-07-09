package vast

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// Run types for CreateInstanceRequest.RunType.
const (
	// RunTypeSSH boots vast's init, runs Onstart, and provides SSH access.
	RunTypeSSH = "ssh"
	// RunTypeArgs runs the image's own ENTRYPOINT/CMD (headless container;
	// Onstart is not applied). The forge's producer images use this.
	RunTypeArgs = "args"
	// RunTypeJupyter boots a Jupyter server.
	RunTypeJupyter = "jupyter"
)

// Instance lifecycle states (Instance.ActualStatus). vast reports a
// created/loading instance with an empty or "loading" actual_status;
// "running" is the only ready state. Per vast docs: once actual_status is
// exited, offline, or unknown the instance will never reach running —
// destroy and re-rent.
const (
	StatusLoading = "loading"
	StatusRunning = "running"
	StatusExited  = "exited"
	StatusOffline = "offline"
	StatusUnknown = "unknown"
)

// Instance is a rented machine as reported by GetInstance/ListInstances.
type Instance struct {
	ID        int64 `json:"id"`
	MachineID int64 `json:"machine_id"`
	HostID    int64 `json:"host_id"`

	// ActualStatus is the observed state (see Status* constants).
	ActualStatus string `json:"actual_status"`
	// IntendedStatus is the desired state ("running"/"stopped").
	IntendedStatus string `json:"intended_status"`
	CurState       string `json:"cur_state"`
	NextState      string `json:"next_state"`
	// StatusMsg carries loading/error detail (image pull progress, docker
	// errors); invaluable for diagnosing stuck instances.
	StatusMsg string `json:"status_msg"`

	Label     string `json:"label"`
	ImageUUID string `json:"image_uuid"`

	GPUName  string  `json:"gpu_name"`
	NumGPUs  int     `json:"num_gpus"`
	GPURAMMB float64 `json:"gpu_ram"`
	GPUUtil  float64 `json:"gpu_util"`

	// DPHTotal is the effective $/hr being billed for compute.
	DPHTotal    float64 `json:"dph_total"`
	DPHBase     float64 `json:"dph_base"`
	StorageCost float64 `json:"storage_cost"`
	DiskSpace   float64 `json:"disk_space"`

	PublicIPAddr string  `json:"public_ipaddr"`
	SSHHost      string  `json:"ssh_host"`
	SSHPort      int     `json:"ssh_port"`
	InetUpCost   float64 `json:"inet_up_cost"`
	InetDownCost float64 `json:"inet_down_cost"`

	// StartDate is a unix timestamp (fractional seconds).
	StartDate   float64 `json:"start_date"`
	Geolocation string  `json:"geolocation"`
}

// StartedAt converts StartDate to a time.Time (zero when unset).
func (i Instance) StartedAt() time.Time {
	if i.StartDate <= 0 {
		return time.Time{}
	}
	return time.Unix(int64(i.StartDate), 0).UTC()
}

// IsRunning reports whether the instance is up and billing for compute.
func (i Instance) IsRunning() bool { return i.ActualStatus == StatusRunning }

// IsTerminal reports whether the instance can never reach running again
// (destroy and re-rent — vast documents exited/offline/unknown as dead ends).
func (i Instance) IsTerminal() bool {
	switch i.ActualStatus {
	case StatusExited, StatusOffline, StatusUnknown:
		return true
	}
	return false
}

// CreateInstanceRequest configures the container launched on an accepted
// offer. Image is required; everything else has serviceable defaults.
//
// SECURITY: the host machine has root over the container — anything in Env
// and Onstart is readable (and tamperable) by the host. Ship only
// single-use, short-TTL, narrowly-scoped credentials.
type CreateInstanceRequest struct {
	// Image is the docker image to run (required).
	Image string
	// Env sets container environment variables. Keys beginning with "-"
	// are passed through as raw docker flags per vast convention
	// (e.g. {"-p 8080:8080": "1"}); prefer Ports for port mappings.
	Env map[string]string
	// Ports adds docker -p mappings (e.g. "8080:8080", "70000:8000/udp").
	Ports []string
	// Onstart is a startup script run by vast's init (<= 4048 chars; gzip
	// +base64 anything larger and decode in the script). Applied for
	// RunTypeSSH/RunTypeJupyter; ignored by RunTypeArgs.
	Onstart string
	// RunType selects the launch mode; defaults to RunTypeArgs (headless:
	// the image's own entrypoint runs) unless Onstart is set, in which
	// case it defaults to RunTypeSSH so the script actually executes.
	RunType string
	// Args replaces the image CMD when RunType is RunTypeArgs.
	Args []string
	// DiskGB is the local disk allocation in GB (default 10). Storage is
	// billed per GB-hour even while the instance is stopped.
	DiskGB float64
	// Label is a free-form tag shown in listings; use it to correlate
	// instances with forge sessions.
	Label string
	// BidPricePerHour places an interruptible bid ($/hr) instead of an
	// on-demand rental. Leave zero for on-demand.
	BidPricePerHour float64
	// TargetState is the state after provisioning ("running" default).
	TargetState string
	// ImageLogin is a docker registry login string for private images
	// ("-u user -p pass registry.example.com").
	ImageLogin string
	// CancelUnavail, when true, fails the create immediately (ErrOfferGone)
	// if the machine cannot start the instance right away, instead of
	// queueing it.
	CancelUnavail bool
}

// CreateInstanceResponse reports the accepted contract.
type CreateInstanceResponse struct {
	Success bool `json:"success"`
	// InstanceID is the new instance (contract) id — vast calls it
	// "new_contract". Use it with GetInstance/DestroyInstance.
	InstanceID int64 `json:"new_contract"`
}

// CreateInstance accepts offer offerID and boots req.Image on it
// (PUT /api/v0/asks/{id}/). On-demand unless req.BidPricePerHour is set.
//
// Offer-gone failures (rented from under you, withdrawn, host offline)
// return an error matching ErrOfferGone: re-search and take the next offer.
// Balance failures match ErrInsufficientCredit. Never retried on 5xx —
// a replay could double-rent; callers own create-retry policy.
func (c *Client) CreateInstance(ctx context.Context, offerID int64, req *CreateInstanceRequest) (*CreateInstanceResponse, error) {
	if offerID <= 0 {
		return nil, &ValidationError{Field: "offerID", Message: "must be a positive offer id"}
	}
	if req == nil || req.Image == "" {
		return nil, &ValidationError{Field: "Image", Message: "docker image is required"}
	}
	if len(req.Onstart) > 4048 {
		return nil, &ValidationError{Field: "Onstart", Message: "exceeds vast's 4048-char limit; gzip+base64 the payload"}
	}

	runType := req.RunType
	if runType == "" {
		if req.Onstart != "" {
			runType = RunTypeSSH
		} else {
			runType = RunTypeArgs
		}
	}

	env := map[string]string{}
	for k, v := range req.Env {
		env[k] = v
	}
	for _, p := range req.Ports {
		env["-p "+p] = "1"
	}

	disk := req.DiskGB
	if disk <= 0 {
		disk = 10
	}
	targetState := req.TargetState
	if targetState == "" {
		targetState = "running"
	}

	body := map[string]interface{}{
		"client_id":    "me",
		"image":        req.Image,
		"env":          env,
		"disk":         disk,
		"runtype":      runType,
		"target_state": targetState,
	}
	if req.Onstart != "" {
		body["onstart"] = req.Onstart
	}
	if len(req.Args) > 0 {
		body["args"] = req.Args
	}
	if req.Label != "" {
		body["label"] = req.Label
	}
	if req.BidPricePerHour > 0 {
		body["price"] = req.BidPricePerHour
	}
	if req.ImageLogin != "" {
		body["image_login"] = req.ImageLogin
	}
	if req.CancelUnavail {
		body["cancel_unavail"] = true
	}

	var resp CreateInstanceResponse
	err := c.do(ctx, http.MethodPut, fmt.Sprintf("/api/v0/asks/%d/", offerID), body, &resp, false)
	if err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.indicatesOfferGone() {
			return nil, &OfferGoneError{OfferID: offerID, Cause: err}
		}
		return nil, err
	}
	if resp.InstanceID == 0 {
		return nil, fmt.Errorf("vast: create succeeded but no new_contract id returned")
	}
	return &resp, nil
}

// GetInstance fetches one instance by id (GET /api/v0/instances/{id}/).
func (c *Client) GetInstance(ctx context.Context, instanceID int64) (*Instance, error) {
	if instanceID <= 0 {
		return nil, &ValidationError{Field: "instanceID", Message: "must be a positive instance id"}
	}
	var resp struct {
		Instances Instance `json:"instances"` // vast nests the single object under "instances"
	}
	if err := c.do(ctx, http.MethodGet, fmt.Sprintf("/api/v0/instances/%d/", instanceID), nil, &resp, true); err != nil {
		return nil, err
	}
	return &resp.Instances, nil
}

// ListInstances returns every instance owned by the account, walking the
// keyset-paginated GET /api/v1/instances/ endpoint (25/page, 2026-04 API
// change) until exhausted.
func (c *Client) ListInstances(ctx context.Context) ([]Instance, error) {
	var out []Instance
	afterToken := ""
	for {
		path := "/api/v1/instances/?limit=25"
		if afterToken != "" {
			path += "&after_token=" + url.QueryEscape(afterToken)
		}
		var resp struct {
			Instances []Instance `json:"instances"`
			NextToken string     `json:"next_token"`
		}
		if err := c.do(ctx, http.MethodGet, path, nil, &resp, true); err != nil {
			return nil, err
		}
		out = append(out, resp.Instances...)
		if resp.NextToken == "" || len(resp.Instances) == 0 {
			return out, nil
		}
		afterToken = resp.NextToken
	}
}

// DestroyInstance permanently destroys an instance and all its data
// (DELETE /api/v0/instances/{id}/). This is the ONLY call that stops
// billing: a merely stopped instance still bills storage. errors.Is(err,
// ErrNotFound) means it's already gone — callers usually treat that as
// success.
func (c *Client) DestroyInstance(ctx context.Context, instanceID int64) error {
	if instanceID <= 0 {
		return &ValidationError{Field: "instanceID", Message: "must be a positive instance id"}
	}
	return c.do(ctx, http.MethodDelete, fmt.Sprintf("/api/v0/instances/%d/", instanceID), nil, nil, true)
}

// WaitForInstanceRunning polls GetInstance until the instance is running,
// reaches a terminal state (returned with a non-nil error matching the
// instance state), or ctx expires. pollInterval <= 0 defaults to 10s.
// The last-observed instance is returned even on error when available.
func (c *Client) WaitForInstanceRunning(ctx context.Context, instanceID int64, pollInterval time.Duration) (*Instance, error) {
	if pollInterval <= 0 {
		pollInterval = 10 * time.Second
	}
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	var last *Instance
	for {
		inst, err := c.GetInstance(ctx, instanceID)
		if err == nil {
			last = inst
			if inst.IsRunning() {
				return inst, nil
			}
			if inst.IsTerminal() {
				return inst, fmt.Errorf("vast: instance %d reached terminal state %q (%s); destroy and re-rent",
					instanceID, inst.ActualStatus, inst.StatusMsg)
			}
		} else if !errors.Is(err, ErrRateLimited) && ctx.Err() == nil {
			// Transient poll errors are tolerated; hard errors bubble up.
			var apiErr *APIError
			if errors.As(err, &apiErr) && !apiErr.IsServerError() {
				return last, err
			}
		}
		select {
		case <-ctx.Done():
			return last, ctx.Err()
		case <-ticker.C:
		}
	}
}
