package inbox

import (
	"context"
	"errors"
	"time"
)

var (
	ErrIdempotencyConflict = errors.New("trace batch idempotency conflict")
	ErrStorageUnavailable  = errors.New("trace inbox storage unavailable")
	ErrBatchNotFound       = errors.New("trace batch not found")
	ErrClosed              = errors.New("trace inbox is closed")
	ErrInvalidEnvelope     = errors.New("invalid trace envelope")
)

type ReceiptOutcome string

const (
	ReceiptAccepted  ReceiptOutcome = "accepted"
	ReceiptDuplicate ReceiptOutcome = "duplicate"
)

type TraceHeaders struct {
	SessionID     string `json:"session_id"`
	Entrypoint    string `json:"entrypoint"`
	RunID         string `json:"run_id"`
	SchemaVersion string `json:"schema_version"`
}

type Envelope struct {
	AccountID      string       `json:"account_id"`
	SessionID      string       `json:"session_id"`
	InstallationID string       `json:"installation_id"`
	BatchID        string       `json:"batch_id"`
	PayloadSHA256  string       `json:"payload_sha256"`
	Payload        []byte       `json:"payload"`
	Headers        TraceHeaders `json:"headers"`
}

type AcceptResult struct {
	BatchID    string
	Outcome    ReceiptOutcome
	AcceptedAt time.Time
}

type Batch struct {
	Envelope
	Sequence   uint64
	AcceptedAt time.Time
	Attempts   int
	LastError  string
	NextRetry  time.Time
}

type Retry struct {
	NextRetry time.Time
	LastError string
}

type StorageGuard interface {
	Check(projectedGrowthBytes int64) error
}

type Options struct {
	ReceiptRetention time.Duration
	MaxDBBytes       int64
	MinFreeBytes     int64
	AllocSize        int
	OpenTimeout      time.Duration
	Now              func() time.Time
	StorageGuard     StorageGuard
}

type Store interface {
	Accept(context.Context, Envelope) (AcceptResult, error)
	PeekEligible(context.Context, time.Time) (*Batch, error)
	MarkRetry(context.Context, string, string, Retry) error
	MarkDelivered(context.Context, string, string, time.Time) error
	MarkQuarantined(context.Context, string, string, string, time.Time) error
	CollectReceipts(context.Context, time.Time) (int, error)
	Sync() error
	Close() error
}
