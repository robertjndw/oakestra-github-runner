package oakestra

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// requestTimeout bounds one HTTP request. The system manager answers synchronously; scheduling
// happens in the background after the call returns.
const requestTimeout = 30 * time.Second

const maxResponseBytes = 32 << 20

// ErrUnauthorized matches an APIError with status 401 under errors.Is.
var ErrUnauthorized = errors.New("oakestra: unauthorized")

// APIError is a non-2xx answer from the system manager.
type APIError struct {
	Op      string
	Status  int
	Message string
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("oakestra: %s: HTTP %d", e.Op, e.Status)
	}
	return fmt.Sprintf("oakestra: %s: HTTP %d: %s", e.Op, e.Status, e.Message)
}

func (e *APIError) Is(target error) bool {
	return target == ErrUnauthorized && e.Status == http.StatusUnauthorized
}

func statusOf(err error) int {
	if ae, ok := errors.AsType[*APIError](err); ok {
		return ae.Status
	}
	return 0
}

// call sends an authenticated request. A 401 is retried once after a full new login.
func (c *Client) call(ctx context.Context, op, method, path string, body, out any) error {
	sess, err := c.session(ctx, "")
	if err != nil {
		return authErr(op, err)
	}
	err = c.send(ctx, op, method, path, sess.token, body, out)
	if !errors.Is(err, ErrUnauthorized) {
		return err
	}
	sess, err = c.session(ctx, sess.token)
	if err != nil {
		return authErr(op, err)
	}
	return c.send(ctx, op, method, path, sess.token, body, out)
}

// send performs one request. bearer is empty for unauthenticated endpoints. Request bodies are
// never logged: they carry the single-use JIT config.
func (c *Client) send(ctx context.Context, op, method, path, bearer string, body, out any) error {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("oakestra: %s: encode request: %w", op, err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return fmt.Errorf("oakestra: %s: %w", op, err)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}

	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("oakestra: %s: %w", op, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("oakestra: %s: read response: %w", op, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &APIError{Op: op, Status: resp.StatusCode, Message: errorMessage(raw)}
	}
	if out == nil || len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("oakestra: %s: decode response: %w", op, err)
	}
	return nil
}

// errorMessage handles the error shapes of the system manager: {"message"} from flask-smorest
// aborts, {"msg"} from flask-jwt-extended, or a plain string.
func errorMessage(raw []byte) string {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) == nil {
		for _, k := range []string{"message", "msg", "description", "error"} {
			v, ok := m[k]
			if !ok {
				continue
			}
			var s string
			if json.Unmarshal(v, &s) == nil {
				return s
			}
			return excerpt(v)
		}
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return excerpt(raw)
}

func excerpt(b []byte) string {
	s := strings.TrimSpace(string(b))
	const limit = 300
	if len(s) > limit {
		s = s[:limit] + "..."
	}
	return s
}
