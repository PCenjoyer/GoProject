package delivery

import (
	"context"
	"encoding/json"
	"time"
)

type Task struct {
	DeliveryID string
	EventID    string
	EventType  string
	Payload    json.RawMessage
	EndpointID string
	URL        string
	Secret     string
	Attempt    int
}

type Result struct {
	StatusCode *int
	Error      string
	StartedAt  time.Time
	FinishedAt time.Time
	NextTryAt  time.Time
	Dead       bool
}

type Store interface {
	Claim(context.Context, string, time.Duration) (Task, bool, error)
	Defer(context.Context, string, Task, time.Time) error
	Finish(context.Context, string, Task, Result) error
}
