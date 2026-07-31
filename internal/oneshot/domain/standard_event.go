package domain

import "time"

// StandardEventArgs contains immutable adapter-normalized event data.
type StandardEventArgs struct {
	RunID                string
	Sequence             int64
	Type                 string
	SourceStreamRecordID *string
	AdapterID            string
	AdapterVersion       string
	Content              map[string]any
	OccurredAt           time.Time
}

// StandardEventSnapshot is the persistence/API representation of a normalized event.
type StandardEventSnapshot struct {
	ID                   string         `json:"id"`
	RunID                string         `json:"run_id"`
	Sequence             int64          `json:"sequence"`
	Type                 string         `json:"type"`
	SourceStreamRecordID *string        `json:"source_stream_record_id,omitempty"`
	AdapterID            string         `json:"adapter_id"`
	AdapterVersion       string         `json:"adapter_version"`
	Content              map[string]any `json:"content"`
	OccurredAt           time.Time      `json:"occurred_at"`
}

// StandardEvent is immutable after construction.
type StandardEvent struct{ snapshot StandardEventSnapshot }

func NewStandardEvent(args StandardEventArgs) (*StandardEvent, error) {
	content, err := cloneJSONMap(args.Content, "standard_event.content")
	if err != nil {
		return nil, err
	}
	snapshot := StandardEventSnapshot{
		ID:                   NewStandardEventID(),
		RunID:                args.RunID,
		Sequence:             args.Sequence,
		Type:                 args.Type,
		SourceStreamRecordID: cloneOptionalString(args.SourceStreamRecordID),
		AdapterID:            args.AdapterID,
		AdapterVersion:       args.AdapterVersion,
		Content:              content,
		OccurredAt:           args.OccurredAt,
	}
	return RestoreStandardEvent(snapshot)
}

func RestoreStandardEvent(snapshot StandardEventSnapshot) (*StandardEvent, error) {
	if err := validateStandardEventSnapshot(snapshot); err != nil {
		return nil, err
	}
	content, err := cloneJSONMap(snapshot.Content, "standard_event.content")
	if err != nil {
		return nil, err
	}
	copySnapshot := snapshot
	copySnapshot.SourceStreamRecordID = cloneOptionalString(snapshot.SourceStreamRecordID)
	copySnapshot.Content = content
	copySnapshot.OccurredAt = snapshot.OccurredAt.UTC()
	return &StandardEvent{snapshot: copySnapshot}, nil
}

func validateStandardEventSnapshot(snapshot StandardEventSnapshot) error {
	if err := validateID(snapshot.ID, standardEventIDPrefix, "standard_event.id"); err != nil {
		return err
	}
	if err := validateID(snapshot.RunID, runIDPrefix, "run_id"); err != nil {
		return err
	}
	if snapshot.Sequence <= 0 {
		return InvalidRequestf("standard_event.sequence must be positive")
	}
	if err := requireNonEmpty(snapshot.Type, "standard_event.type"); err != nil {
		return err
	}
	if snapshot.SourceStreamRecordID != nil {
		if err := validateID(*snapshot.SourceStreamRecordID, streamRecordIDPrefix, "source_stream_record_id"); err != nil {
			return err
		}
	}
	if err := requireNonEmpty(snapshot.AdapterID, "adapter_id"); err != nil {
		return err
	}
	if err := requireNonEmpty(snapshot.AdapterVersion, "adapter_version"); err != nil {
		return err
	}
	if _, err := cloneJSONMap(snapshot.Content, "standard_event.content"); err != nil {
		return err
	}
	_, err := normalizeTime(snapshot.OccurredAt, "occurred_at")
	return err
}

func (e *StandardEvent) Snapshot() StandardEventSnapshot {
	out := e.snapshot
	out.SourceStreamRecordID = cloneOptionalString(e.snapshot.SourceStreamRecordID)
	out.Content, _ = cloneJSONMap(e.snapshot.Content, "standard_event.content")
	return out
}

// ValidateNextStandardEvent enforces per-Run append ordering for normalized events.
func ValidateNextStandardEvent(previous, next StandardEventSnapshot) error {
	if err := validateStandardEventSnapshot(previous); err != nil {
		return err
	}
	if err := validateStandardEventSnapshot(next); err != nil {
		return err
	}
	if previous.RunID != next.RunID {
		return InvalidRequestf("StandardEvent sequence comparison requires the same Run")
	}
	if next.Sequence <= previous.Sequence {
		return InvalidRequestf("StandardEvent sequence must be strictly increasing within a Run")
	}
	return nil
}
