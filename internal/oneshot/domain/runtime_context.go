package domain

import (
	"path/filepath"
	"strings"
	"time"
)

// RuntimeContextStatus is the persisted provider continuity state.
type RuntimeContextStatus string

const (
	ContextActive  RuntimeContextStatus = "active"
	ContextBusy    RuntimeContextStatus = "busy"
	ContextInvalid RuntimeContextStatus = "invalid"
	ContextRevoked RuntimeContextStatus = "revoked"
)

var allRuntimeContextStatuses = []RuntimeContextStatus{
	ContextActive, ContextBusy, ContextInvalid, ContextRevoked,
}

func (s RuntimeContextStatus) String() string { return string(s) }

func (s RuntimeContextStatus) Valid() bool {
	switch s {
	case ContextActive, ContextBusy, ContextInvalid, ContextRevoked:
		return true
	default:
		return false
	}
}

func (s RuntimeContextStatus) Terminal() bool {
	return s == ContextInvalid || s == ContextRevoked
}

var runtimeContextTransitions = map[RuntimeContextStatus]map[RuntimeContextStatus]struct{}{
	ContextActive: {
		ContextBusy: {}, ContextInvalid: {}, ContextRevoked: {},
	},
	ContextBusy: {
		ContextActive: {}, ContextInvalid: {}, ContextRevoked: {},
	},
}

// CanRuntimeContextTransition reports whether a state edge exists in the frozen contract.
func CanRuntimeContextTransition(from, to RuntimeContextStatus) bool {
	toSet, ok := runtimeContextTransitions[from]
	if !ok {
		return false
	}
	_, ok = toSet[to]
	return ok
}

// RuntimeContextArgs contains immutable provider continuity metadata.
type RuntimeContextArgs struct {
	Owner             Owner
	ProjectID         string
	ProviderID        string
	ProviderContextID string
	WorkspacePath     string
}

// RuntimeContextSnapshot is the storage/API representation of RuntimeContext.
type RuntimeContextSnapshot struct {
	ID                string               `json:"id"`
	PrincipalKind     PrincipalKind        `json:"principal_kind"`
	PrincipalID       string               `json:"principal_id"`
	ProjectID         string               `json:"project_id"`
	ProviderID        string               `json:"provider_id"`
	ProviderContextID string               `json:"provider_context_id"`
	WorkspacePath     string               `json:"workspace_path"`
	Status            RuntimeContextStatus `json:"status"`
	Version           int64                `json:"version"`
	CreatedAt         time.Time            `json:"created_at"`
	UpdatedAt         time.Time            `json:"updated_at"`
}

// RuntimeContext stores provider-native resume metadata only. It has no live process or PTY state.
type RuntimeContext struct {
	id                string
	owner             Owner
	projectID         string
	providerID        string
	providerContextID string
	workspacePath     string
	status            RuntimeContextStatus
	version           int64
	createdAt         time.Time
	updatedAt         time.Time
}

// NewRuntimeContext creates an active provider continuity context.
func NewRuntimeContext(args RuntimeContextArgs, now time.Time) (*RuntimeContext, error) {
	normalizedNow, err := normalizeTime(now, "created_at")
	if err != nil {
		return nil, err
	}
	if err := args.Owner.Validate(); err != nil {
		return nil, err
	}
	if err := requireNonEmpty(args.ProjectID, "project_id"); err != nil {
		return nil, err
	}
	if err := requireNonEmpty(args.ProviderID, "provider_id"); err != nil {
		return nil, err
	}
	if err := requireNonEmpty(args.ProviderContextID, "provider_context_id"); err != nil {
		return nil, err
	}
	if err := validateWorkspacePath(args.WorkspacePath); err != nil {
		return nil, err
	}
	return &RuntimeContext{
		id:                NewRuntimeContextID(),
		owner:             args.Owner,
		projectID:         args.ProjectID,
		providerID:        args.ProviderID,
		providerContextID: args.ProviderContextID,
		workspacePath:     filepath.Clean(args.WorkspacePath),
		status:            ContextActive,
		version:           1,
		createdAt:         normalizedNow,
		updatedAt:         normalizedNow,
	}, nil
}

func validateWorkspacePath(value string) error {
	if err := requireNonEmpty(value, "workspace_path"); err != nil {
		return err
	}
	if !filepath.IsAbs(value) && !isWindowsAbsolutePath(value) {
		return InvalidRequestf("workspace_path must be an absolute server-resolved path")
	}
	cleaned := filepath.Clean(value)
	if cleaned == string(filepath.Separator) || strings.TrimSpace(cleaned) == "" {
		return InvalidRequestf("workspace_path must identify a controlled workspace")
	}
	return nil
}

// RestoreRuntimeContext validates and restores persisted continuity metadata.
func RestoreRuntimeContext(snapshot RuntimeContextSnapshot) (*RuntimeContext, error) {
	if err := validateRuntimeContextSnapshot(snapshot); err != nil {
		return nil, err
	}
	return &RuntimeContext{
		id:                snapshot.ID,
		owner:             Owner{Kind: snapshot.PrincipalKind, ID: snapshot.PrincipalID},
		projectID:         snapshot.ProjectID,
		providerID:        snapshot.ProviderID,
		providerContextID: snapshot.ProviderContextID,
		workspacePath:     filepath.Clean(snapshot.WorkspacePath),
		status:            snapshot.Status,
		version:           snapshot.Version,
		createdAt:         snapshot.CreatedAt.UTC(),
		updatedAt:         snapshot.UpdatedAt.UTC(),
	}, nil
}

func validateRuntimeContextSnapshot(snapshot RuntimeContextSnapshot) error {
	if err := validateID(snapshot.ID, runtimeContextIDPrefix, "runtime_context.id"); err != nil {
		return err
	}
	if err := (Owner{Kind: snapshot.PrincipalKind, ID: snapshot.PrincipalID}).Validate(); err != nil {
		return err
	}
	if err := requireNonEmpty(snapshot.ProjectID, "project_id"); err != nil {
		return err
	}
	if err := requireNonEmpty(snapshot.ProviderID, "provider_id"); err != nil {
		return err
	}
	if err := requireNonEmpty(snapshot.ProviderContextID, "provider_context_id"); err != nil {
		return err
	}
	if err := validateWorkspacePath(snapshot.WorkspacePath); err != nil {
		return err
	}
	if !snapshot.Status.Valid() {
		return InvalidRequestf("invalid runtime_context.status %q", snapshot.Status)
	}
	if snapshot.Version < 1 {
		return InvalidRequestf("runtime_context.version must be at least 1")
	}
	createdAt, err := normalizeTime(snapshot.CreatedAt, "created_at")
	if err != nil {
		return err
	}
	updatedAt, err := normalizeTime(snapshot.UpdatedAt, "updated_at")
	if err != nil {
		return err
	}
	if updatedAt.Before(createdAt) {
		return InvalidRequestf("updated_at must not be before created_at")
	}
	return nil
}

// Snapshot returns a defensive persistence/API copy.
func (c *RuntimeContext) Snapshot() RuntimeContextSnapshot {
	return RuntimeContextSnapshot{
		ID:                c.id,
		PrincipalKind:     c.owner.Kind,
		PrincipalID:       c.owner.ID,
		ProjectID:         c.projectID,
		ProviderID:        c.providerID,
		ProviderContextID: c.providerContextID,
		WorkspacePath:     c.workspacePath,
		Status:            c.status,
		Version:           c.version,
		CreatedAt:         c.createdAt,
		UpdatedAt:         c.updatedAt,
	}
}

func validateContextCompatibility(owner Owner, projectID, providerID string, context RuntimeContextSnapshot) error {
	if err := validateRuntimeContextSnapshot(context); err != nil {
		return err
	}
	if context.PrincipalKind != owner.Kind || context.PrincipalID != owner.ID ||
		context.ProjectID != projectID || context.ProviderID != providerID {
		return contextOwnerMismatch()
	}
	return nil
}

func (c *RuntimeContext) assertCompatible(owner Owner, projectID, providerID string) error {
	if !c.owner.Equal(owner) || c.projectID != projectID || c.providerID != providerID {
		return contextOwnerMismatch()
	}
	return nil
}

func (c *RuntimeContext) transition(to RuntimeContextStatus, command string, expectedVersion int64, now time.Time) error {
	if expectedVersion != c.version {
		return runConflict("RuntimeContext optimistic version conflict")
	}
	if !CanRuntimeContextTransition(c.status, to) {
		return invalidTransition("RuntimeContext", c.status.String(), to.String(), command)
	}
	normalizedNow, err := normalizeTime(now, "updated_at")
	if err != nil {
		return err
	}
	if normalizedNow.Before(c.updatedAt) {
		return InvalidRequestf("updated_at must not move backwards")
	}
	c.status = to
	c.version++
	c.updatedAt = normalizedNow
	return nil
}

// Acquire performs active -> busy after exact owner/project/provider and version checks.
func (c *RuntimeContext) Acquire(owner Owner, projectID, providerID string, expectedVersion int64, now time.Time) error {
	if err := c.assertCompatible(owner, projectID, providerID); err != nil {
		return err
	}
	if c.status == ContextBusy {
		return runConflict("RuntimeContext already has an active Run")
	}
	return c.transition(ContextBusy, "acquire", expectedVersion, now)
}

// Release performs busy -> active when the owning Run is terminal and resumable.
func (c *RuntimeContext) Release(expectedVersion int64, now time.Time) error {
	return c.transition(ContextActive, "release", expectedVersion, now)
}

// Invalidate permanently marks the context unusable.
func (c *RuntimeContext) Invalidate(expectedVersion int64, now time.Time) error {
	return c.transition(ContextInvalid, "invalidate", expectedVersion, now)
}

// Revoke permanently removes owner-authorized use of the context.
func (c *RuntimeContext) Revoke(expectedVersion int64, now time.Time) error {
	return c.transition(ContextRevoked, "revoke", expectedVersion, now)
}
