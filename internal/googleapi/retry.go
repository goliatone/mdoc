package googleapi

import (
	"context"
	"errors"
)

type RetryPolicy struct {
	Attempts int
	Backoff  func(context.Context, int) error
}

func (p RetryPolicy) Do(ctx context.Context, operation func() error) error {
	attempts := p.Attempts
	if attempts <= 0 {
		attempts = 3
	}
	backoff := p.Backoff
	if backoff == nil {
		backoff = defaultBackoff
	}
	var last error
	for attempt := 0; attempt < attempts; attempt++ {
		last = operation()
		if last == nil || !IsRetryable(last) || attempt == attempts-1 {
			return last
		}
		if err := backoff(ctx, attempt); err != nil {
			return err
		}
	}
	return last
}

func retryValue[T any](ctx context.Context, operation string, call func() (T, error)) (T, error) {
	var result T
	err := (RetryPolicy{}).Do(ctx, func() error {
		value, err := call()
		if err != nil {
			return classify(operation, err)
		}
		result = value
		return nil
	})
	return result, err
}

func IsRetryable(err error) bool {
	var remote *Error
	return errors.As(err, &remote) && (remote.Kind == KindRetryable || remote.Kind == KindQuota)
}
