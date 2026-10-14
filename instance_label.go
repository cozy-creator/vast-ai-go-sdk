package vast

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

// LabelInstance updates only an instance's console label. The instance's
// desired state and container configuration are unchanged.
func (c *Client) LabelInstance(ctx context.Context, instanceID int64, label string) error {
	if instanceID <= 0 {
		return &ValidationError{Field: "instanceID", Message: "must be a positive instance id"}
	}
	if strings.TrimSpace(label) == "" {
		return &ValidationError{Field: "label", Message: "must not be empty"}
	}
	var response struct {
		Success bool   `json:"success"`
		Message string `json:"msg"`
	}
	if err := c.do(ctx, http.MethodPut, fmt.Sprintf("/api/v0/instances/%d/", instanceID), map[string]string{"label": label}, &response, true); err != nil {
		return err
	}
	if !response.Success {
		return fmt.Errorf("vast: label instance %d refused: %s", instanceID, response.Message)
	}
	return nil
}
