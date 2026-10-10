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

	// A deployment's own two streams.
	//
	// Split because they answer different questions at different rates: the operation
	// under way reports a step every few seconds and is the only thing on the page that
	// changes, while the history of what has been deployed changes once, at the end.
	// One event for both would mean a page redrawing a list of twenty deployments
	// every time one pod came up — which is exactly what it was doing.
	// EventDeployPlan is the list of steps this particular deployment will go through,
	// sent once before any of them happens.
	//
	// A separate kind rather than a field on the first progress event, because it is a
	// different kind of fact: this is the whole plan, and it is true before the first
	// step is taken. A client that had to infer it from progress would be inventing a
	// list and correcting it as it went.
	EventDeployPlan   EventKind = "deploy.plan"
	EventDeployOperation EventKind = "deploy.operation"
	EventDeployHistory   EventKind = "deploy.history"
	// A deployment joined the queue and still has not started. It changes the list
	// without producing progress lines, so it needs a signal of its own.
	EventDeployQueued EventKind = "deploy.queued"

	// Module events carry no project: a module belongs to the instance rather than
	// to anything inside it, and the only page that watches them is the
	// administrator's.
	EventModuleRegistered EventKind = "module.registered"
	EventModuleUpdated    EventKind = "module.updated"
	EventModuleRemoved    EventKind = "module.removed"
	// EventModuleReported is a module saying that something it owns changed.
	//
	// Most changes travel through the core and are announced by it. This one cannot:
	// the module is asked by a browser directly, with a credential the core minted
	// and the core cannot present. So the module reports it — it holds its own token
	// and can therefore be believed about itself, which is the only thing it is
	// believed about.
	EventModuleReported EventKind = "module.reported"
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
