package domain

import (
	"encoding/json"
	"time"
)

type Endpoint struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	URL       string    `json:"url"`
	Secret    string    `json:"-"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Event struct {
	ID             string          `json:"id"`
	IdempotencyKey string          `json:"idempotency_key"`
	Type           string          `json:"type"`
	Payload        json.RawMessage `json:"payload"`
	CreatedAt      time.Time       `json:"created_at"`
}

type DeliveryStatus string

const (
	DeliveryPending    DeliveryStatus = "pending"
	DeliveryDelivering DeliveryStatus = "delivering"
	DeliverySucceeded  DeliveryStatus = "succeeded"
	DeliveryRetrying   DeliveryStatus = "retrying"
	DeliveryDead       DeliveryStatus = "dead"
)

type Delivery struct {
	ID             string         `json:"id"`
	EventID        string         `json:"event_id"`
	EndpointID     string         `json:"endpoint_id"`
	Status         DeliveryStatus `json:"status"`
	AttemptCount   int            `json:"attempt_count"`
	NextAttemptAt  time.Time      `json:"next_attempt_at"`
	LastStatusCode *int           `json:"last_status_code,omitempty"`
	LastError      *string        `json:"last_error,omitempty"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
}
