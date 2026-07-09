package vast

import (
	"context"
	"net/http"
)

// User is the authenticated account (GET /api/v0/users/current/).
type User struct {
	ID    int64  `json:"id"`
	Email string `json:"email"`
	// Balance is the current prepaid credit in USD.
	Balance float64 `json:"balance"`
	// Credit is promotional/awarded credit in USD, when present.
	Credit float64 `json:"credit"`
}

// GetCurrentUser returns the account that owns the API key.
func (c *Client) GetCurrentUser(ctx context.Context) (*User, error) {
	var user User
	if err := c.do(ctx, http.MethodGet, "/api/v0/users/current/", nil, &user, true); err != nil {
		return nil, err
	}
	return &user, nil
}

// Balance returns the account's current credit balance in USD. The forge's
// spend guardrails poll this before opening a session.
func (c *Client) Balance(ctx context.Context) (float64, error) {
	user, err := c.GetCurrentUser(ctx)
	if err != nil {
		return 0, err
	}
	return user.Balance, nil
}
