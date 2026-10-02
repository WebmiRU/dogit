package models

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// EventKind enumerates the internal event types published on the event bus.
type EventKind string

const (
	EventPush                EventKind = "push"
	EventPipelineCreated     EventKind = "pipeline.created"
	EventPipelineUpdated     EventKind = "pipeline.updated"
	EventJobCreated          EventKind = "job.created"
	EventJobUpdated          EventKind = "job.updated"
	EventMergeRequestChanged EventKind = "merge_request.changed"
)

// Event is a row in the durable event log. The post-receive hook writes events
// directly to Postgres instead of calling the web process over HTTP, so that
// pushes keep working even when the web tier is down.
type Event struct {
	ID        int64           `json:"id"`
	Kind      EventKind       `json:"kind"`
	ProjectID *uuid.UUID      `json:"project_id,omitempty"`
	ActorID   *uuid.UUID      `json:"actor_id,omitempty"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt time.Time       `json:"created_at"`
}
