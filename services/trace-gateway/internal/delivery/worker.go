// Package delivery sends durably accepted trace batches to the upstream
// Langfuse endpoint without weakening the inbox's FIFO and receipt guarantees.
package delivery

import (
	"context"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Ansatz-agent/ansatz-agent-platform/services/trace-gateway/internal/inbox"
)

const (
	defaultBaseRetry = time.Second
	defaultMaxRetry  = 5 * time.Minute
	// defaultRetryAfterCap bounds how long an upstream Retry-After header can
	// delay the FIFO head, so one absurd delta-seconds value or far-future
	// HTTP date cannot stall strict FIFO delivery indefinitely.
	defaultRetryAfterCap = time.Hour
	// defaultOperatorRetryLimit is the total delivery attempts granted to
	// operator-fixable rejections (401/403/404) before quarantine.
	defaultOperatorRetryLimit = 6
	// minimumRetryDelay prevents a zero-jitter retry from immediately spinning
	// against the upstream while preserving any longer valid Retry-After value.
	minimumRetryDelay = time.Millisecond
)

// Upstream sends canonical OTLP protobuf bytes. Implementations must not
// return or log upstream response bodies through this interface.
type Upstream interface {
	Send(context.Context, []byte) (Response, error)
}

// Response contains only the delivery fields needed for a retry decision.
type Response struct {
	Status     int
	RetryAfter string
}

// Options supplies deterministic seams for tests and retry scheduling.
type Options struct {
	Now       func() time.Time
	Random    func() float64
	BaseRetry time.Duration
	MaxRetry  time.Duration
	// RetryAfterCap is the ceiling applied to parsed upstream Retry-After
	// values. Zero selects defaultRetryAfterCap.
	RetryAfterCap time.Duration
	// OperatorRetryLimit is the total number of delivery attempts allowed for
	// operator-fixable upstream rejections (401/403/404) before the batch is
	// quarantined. Zero selects defaultOperatorRetryLimit.
	OperatorRetryLimit int
}

// Worker owns one coalesced delivery loop for an inbox.
type Worker struct {
	store              inbox.Store
	upstream           Upstream
	now                func() time.Time
	random             func() float64
	base               time.Duration
	max                time.Duration
	retryAfterCap      time.Duration
	operatorRetryLimit int
	trigger            chan struct{}
}

// New constructs a worker. Run begins delivery immediately; Trigger can be
// used to coalesce subsequent durable-acceptance wakeups.
func New(store inbox.Store, upstream Upstream, options Options) *Worker {
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Random == nil {
		options.Random = rand.Float64
	}
	if options.BaseRetry <= 0 {
		options.BaseRetry = defaultBaseRetry
	}
	if options.MaxRetry <= 0 {
		options.MaxRetry = defaultMaxRetry
	}
	if options.BaseRetry > options.MaxRetry {
		options.BaseRetry = options.MaxRetry
	}
	if options.RetryAfterCap <= 0 {
		options.RetryAfterCap = defaultRetryAfterCap
	}
	if options.OperatorRetryLimit <= 0 {
		options.OperatorRetryLimit = defaultOperatorRetryLimit
	}
	return &Worker{
		store: store, upstream: upstream, now: options.Now, random: options.Random,
		base: options.BaseRetry, max: options.MaxRetry,
		retryAfterCap: options.RetryAfterCap, operatorRetryLimit: options.OperatorRetryLimit,
		trigger: make(chan struct{}, 1),
	}
}

// Trigger requests delivery without allowing a burst of acceptances to queue
// unbounded wakeups.
func (w *Worker) Trigger() {
	select {
	case w.trigger <- struct{}{}:
	default:
	}
}

// Run delivers the FIFO head until it is delayed or the inbox is empty, then
// sleeps until the persisted retry deadline or a coalesced trigger. Context
// cancellation is a normal shutdown and returns nil.
func (w *Worker) Run(ctx context.Context) error {
	for {
		if ctx.Err() != nil {
			return nil
		}
		delivered, err := w.deliverHead(ctx, w.now())
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		if delivered {
			continue
		}

		nextRetry, err := w.nextRetryAt(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		if err := w.wait(ctx, nextRetry); err != nil {
			return err
		}
	}
}

func (w *Worker) deliverHead(ctx context.Context, now time.Time) (bool, error) {
	batch, err := w.store.PeekEligible(ctx, now)
	if err != nil || batch == nil {
		return false, err
	}
	response, sendErr := w.upstream.Send(ctx, batch.Payload)
	switch classify(response.Status, sendErr, batch.Attempts, w.operatorRetryLimit) {
	case deliverySuccess:
		return true, w.store.MarkDelivered(ctx, batch.AccountID, batch.BatchID, now)
	case deliveryPermanent:
		return true, w.store.MarkQuarantined(ctx, batch.AccountID, batch.BatchID, safeErrorClass(response.Status), now)
	default:
		retryNow := w.now()
		retry := inbox.Retry{
			NextRetry: fullJitter(batch.Attempts, retryNow, response.RetryAfter, w.base, w.max, w.retryAfterCap, w.random),
			LastError: safeRetryErrorClass(response.Status, sendErr),
		}
		return true, w.store.MarkRetry(ctx, batch.AccountID, batch.BatchID, retry)
	}
}

func (w *Worker) nextRetryAt(ctx context.Context) (time.Time, error) {
	return w.store.NextRetryAt(ctx)
}

func (w *Worker) wait(ctx context.Context, nextRetry time.Time) error {
	if nextRetry.IsZero() {
		select {
		case <-ctx.Done():
			return nil
		case <-w.trigger:
			return nil
		}
	}
	delay := nextRetry.Sub(w.now())
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return nil
	case <-w.trigger:
		return nil
	case <-timer.C:
		return nil
	}
}

type deliveryClass uint8

const (
	deliveryRetryable deliveryClass = iota
	deliverySuccess
	deliveryPermanent
)

// classify decides the outcome of one delivery attempt. Transport failures,
// 408, 429, and 5xx are always retryable. Operator-fixable rejections
// (401/403/404, e.g. rotated upstream credentials or a moved endpoint) are
// retried until the batch has consumed operatorRetryLimit total attempts so a
// misconfiguration window does not quarantine traffic, while keeping the FIFO
// head stall bounded. Remaining 4xx statuses are permanent.
func classify(status int, sendErr error, attempts, operatorRetryLimit int) deliveryClass {
	if sendErr != nil || status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= 500 {
		return deliveryRetryable
	}
	if status >= 200 && status < 300 {
		return deliverySuccess
	}
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
		if attempts+1 < operatorRetryLimit {
			return deliveryRetryable
		}
		return deliveryPermanent
	}
	if status >= 400 && status < 500 {
		return deliveryPermanent
	}
	return deliveryRetryable
}

func safeErrorClass(status int) string {
	return "upstream_" + strconv.Itoa(status)
}

func safeRetryErrorClass(status int, sendErr error) string {
	if sendErr != nil {
		return "upstream_network"
	}
	return safeErrorClass(status)
}

func fullJitter(attempts int, now time.Time, retryAfter string, base, max, retryAfterCap time.Duration, random func() float64) time.Time {
	delay := base
	for range attempts {
		if delay >= max/2 {
			delay = max
			break
		}
		delay *= 2
	}
	if delay > max {
		delay = max
	}
	jitter := random()
	if jitter < 0 {
		jitter = 0
	} else if jitter > 1 {
		jitter = 1
	}
	delay = time.Duration(float64(delay) * jitter)
	retryDelay := parseRetryAfter(retryAfter, now)
	if retryDelay > retryAfterCap {
		retryDelay = retryAfterCap
	}
	if retryDelay > delay {
		delay = retryDelay
	}
	if delay < minimumRetryDelay {
		delay = minimumRetryDelay
	}
	return now.Add(delay).UTC()
}

func parseRetryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
		if seconds <= 0 || seconds > int64((time.Duration(1<<63-1))/time.Second) {
			return 0
		}
		return time.Duration(seconds) * time.Second
	}
	when, err := http.ParseTime(value)
	if err != nil || !when.After(now) {
		return 0
	}
	return when.Sub(now)
}
