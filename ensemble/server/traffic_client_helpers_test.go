package server_test

import (
	"context"
	"strconv"
	"time"
)

func itoa(v uint64) string { return strconv.FormatUint(v, 10) }

func contextWithTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}
