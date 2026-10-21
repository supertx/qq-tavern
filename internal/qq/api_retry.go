package qq

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"
)

type APIError struct {
	Status, Code int
	RetryAfter   string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("QQ API rejected: HTTP %d code %d", e.Status, e.Code)
}
func (g *Gateway) invalidateToken() {
	g.tokenMu.Lock()
	g.tokenExpires = time.Time{}
	g.tokenMu.Unlock()
}

// Only retry explicit rejection. Transport errors and 5xx may have delivered
// the message, so the durable send receipt must remain uncertain in that case.
func (g *Gateway) api(ctx context.Context, method, path string, body, target any) error {
	for attempt := 0; ; attempt++ {
		err := g.apiOnce(ctx, method, path, body, target)
		var failure *APIError
		if attempt != 0 || !errors.As(err, &failure) {
			return err
		}
		switch failure.Status {
		case 401:
			g.invalidateToken()
		case 429:
			seconds, e := strconv.Atoi(failure.RetryAfter)
			if e != nil || seconds <= 0 {
				seconds = 1
			}
			if seconds > 5 {
				return err
			}
			timer := time.NewTimer(time.Duration(seconds) * time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		default:
			return err
		}
	}
}
