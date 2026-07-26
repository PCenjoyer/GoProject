package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/PCenjoyer/GoProject/internal/domain"
)

var (
	ErrNotFound        = errors.New("not found")
	ErrInvalidEndpoint = errors.New("one or more endpoints do not exist or are disabled")
)

type CreateEndpointParams struct {
	Name   string
	URL    string
	Secret string
}

type CreateEventParams struct {
	IdempotencyKey string
	Type           string
	Payload        json.RawMessage
	EndpointIDs    []string
}

type EventResult struct {
	Event     domain.Event
	Duplicate bool
}

type DeliveryFilter struct {
	Status domain.DeliveryStatus
	Limit  int
}

type Store interface {
	CreateEndpoint(context.Context, CreateEndpointParams) (domain.Endpoint, error)
	ListEndpoints(context.Context, int) ([]domain.Endpoint, error)
	CreateEvent(context.Context, CreateEventParams) (EventResult, error)
	GetEvent(context.Context, string) (domain.Event, error)
	ListDeliveries(context.Context, DeliveryFilter) ([]domain.Delivery, error)
}
