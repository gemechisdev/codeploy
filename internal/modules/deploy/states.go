// Package deploy is the deployment engine (plan 3.8): a durable state machine run as a job on the existing queue.
//
//	queued -> preparing -> building -> starting -> verifying -> switching -> observing -> succeeded
//
// with terminal side-states failed, cancelled, interrupted and rolled_back. Every running deployment writes `step`
// and `heartbeat_at`. Routing derives from resources.active_deployment_id only, and only the switching step writes
// it, so a deployment that fails earlier never moved traffic.
package deploy

// Status values match the deployments.status CHECK constraint and the OpenAPI enum.
const (
	StQueued      = "queued"
	StPreparing   = "preparing"
	StBuilding    = "building"
	StStarting    = "starting"
	StVerifying   = "verifying"
	StSwitching   = "switching"
	StObserving   = "observing"
	StSucceeded   = "succeeded"
	StFailed      = "failed"
	StCancelled   = "cancelled"
	StInterrupted = "interrupted"
	StRolledBack  = "rolled_back"
)

// Triggers.
const (
	TrManual     = "manual"
	TrWebhook    = "webhook"
	TrDeployHook = "deploy_hook"
	TrRollback   = "rollback"
)

// JobKind is the queue job kind; the payload is {"deployment_id": "..."}.
const JobKind = "deploy.run"

// Terminal reports whether a status is final: the engine never writes a deployment again once it is.
func Terminal(s string) bool {
	switch s {
	case StSucceeded, StFailed, StCancelled, StInterrupted, StRolledBack:
		return true
	}
	return false
}

// Running reports the states in which a deployment owns containers and the worker: preparing .. observing.
func Running(s string) bool {
	switch s {
	case StPreparing, StBuilding, StStarting, StVerifying, StSwitching, StObserving:
		return true
	}
	return false
}

// InProgress is every non-terminal state (the UI's IN_PROGRESS).
func InProgress(s string) bool { return s == StQueued || Running(s) }

// Cancellable mirrors the UI's CANCELLABLE: once traffic has switched there is nothing to cancel, the
// observation window already protects the user with an automatic rollback.
func Cancellable(s string) bool {
	switch s {
	case StQueued, StPreparing, StBuilding, StStarting, StVerifying:
		return true
	}
	return false
}

// runningStates is the SQL list for "in flight".
const runningStates = "'preparing','building','starting','verifying','switching','observing'"
