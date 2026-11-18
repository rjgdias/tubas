package llm

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go/v2"
)

// MaxAttempts is how many times a call is tried, as Claude Code does.
var MaxAttempts = 10

// retryBase is the first wait without a retry-after (tests shorten it).
var retryBase = time.Second

// HTTPError is a provider's error answer.
type HTTPError struct {
	Status     int
	Body       string
	RetryAfter time.Duration
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("the provider answered %d: %s", e.Status, e.Body)
}

// StatusOf is an error's HTTP status (0 when it has none).
func StatusOf(err error) int {
	var h *HTTPError
	if errors.As(err, &h) {
		return h.Status
	}
	var a *anthropic.Error
	if errors.As(err, &a) {
		return a.StatusCode
	}
	return 0
}

// retryable says whether an error deserves another try, and how long to
// wait first when the provider says.
func retryable(err error) (bool, int, time.Duration) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false, 0, 0
	}
	status := StatusOf(err)
	var after time.Duration
	var h *HTTPError
	if errors.As(err, &h) {
		after = h.RetryAfter
	}
	var a *anthropic.Error
	if errors.As(err, &a) && a.Response != nil {
		after = retryAfter(a.Response.Header)
	}
	switch {
	case status == 0:
		// No answer at all, or a stream that broke: worth another try,
		// unless it is how the request itself was written.
		msg := strings.ToLower(err.Error())
		if strings.Contains(msg, "unsupported protocol") || strings.Contains(msg, "invalid") {
			return false, 0, 0
		}
		return true, 0, after
	case status == 408, status == 409, status == 429, status >= 500:
		return true, status, after
	}
	return false, status, 0
}

// retryAfter reads retry-after-ms or retry-after (seconds).
func retryAfter(h http.Header) time.Duration {
	if ms, err := strconv.Atoi(h.Get("retry-after-ms")); err == nil && ms > 0 {
		return time.Duration(ms) * time.Millisecond
	}
	if s, err := strconv.Atoi(h.Get("retry-after")); err == nil && s > 0 {
		return time.Duration(s) * time.Second
	}
	return 0
}

// reasonOf names a failure the way the screen says it.
func reasonOf(status int, err error) string {
	switch {
	case status == 0:
		return "unreachable"
	case status == 429:
		return "rate_limited"
	case status == 529:
		return "overloaded"
	case status >= 500:
		return "server_error"
	}
	return http.StatusText(status)
}

// withRetries runs one attempt after another until one works, a failure
// isn't worth retrying, or MaxAttempts is reached. attempt says whether
// it already showed text, so the screen can drop it.
func withRetries(ctx context.Context, ev Events, attempt func() (Response, bool, error)) (Response, error) {
	var last error
	for n := 1; n <= MaxAttempts; n++ {
		resp, partial, err := attempt()
		if err == nil {
			return resp, nil
		}
		last = err
		again, status, after := retryable(err)
		if !again || n == MaxAttempts {
			return Response{}, err
		}
		delay := after
		if delay == 0 {
			delay = retryBase << min(n-1, 5) // 1s, 2s, 4s… 32s
			delay += time.Duration(rand.Int64N(int64(delay)/4 + 1))
		}
		if ev.Retry != nil {
			ev.Retry(Retry{Attempt: n, Max: MaxAttempts - 1, Status: status, Reason: reasonOf(status, err), Delay: delay, Partial: partial})
		}
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return Response{}, ctx.Err()
		}
	}
	return Response{}, last
}
