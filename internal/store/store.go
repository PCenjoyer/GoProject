package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/PCenjoyer/GoProject/internal/domain"
)

var (
	ErrNotFound        = errors.New("не найдено")
	ErrInvalidEndpoint = errors.New("одна или несколько точек назначения не существуют или отключены")
)

type CreateEndpointParams struct {
	TenantID string
	Name     string
	URL      string
	Secret   string
}

type CreateEventParams struct {
	TenantID       string
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
	TenantID string
	Status   domain.DeliveryStatus
	Limit    int
}

type Store interface {
	CreateEndpoint(context.Context, CreateEndpointParams) (domain.Endpoint, error)
	ListEndpoints(context.Context, string, int) ([]domain.Endpoint, error)
	CreateEvent(context.Context, CreateEventParams) (EventResult, error)
	GetEvent(context.Context, string, string) (domain.Event, error)
	ListDeliveries(context.Context, DeliveryFilter) ([]domain.Delivery, error)
	ReplayDelivery(context.Context, string, string) error
}
