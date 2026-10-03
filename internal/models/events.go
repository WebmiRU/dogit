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
	EventProjectUpdated      EventKind = "project.updated"

	// Module events carry no project: a module belongs to the instance rather than
	// to anything inside it, and the only page that watches them is the
	// administrator's.
	EventModuleRegistered EventKind = "module.registered"
	EventModuleUpdated    EventKind = "module.updated"
	EventModuleRemoved    EventKind = "module.removed"
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
