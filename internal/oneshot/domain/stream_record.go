package domain

import "time"

// StreamKind identifies the raw child-process pipe.
type StreamKind string

const (
	StreamStdout StreamKind = "stdout"
	StreamStderr StreamKind = "stderr"
)

func (s StreamKind) String() string { return string(s) }
func (s StreamKind) Valid() bool    { return s == StreamStdout || s == StreamStderr }

// DecodeStatus records whether raw bytes could be represented as text.
type DecodeStatus string

const (
	DecodeValidUTF8 DecodeStatus = "valid_utf8"
	DecodeLossyUTF8 DecodeStatus = "lossy_utf8"
	DecodeBinary    DecodeStatus = "binary"
)

func (s DecodeStatus) String() string { return string(s) }
func (s DecodeStatus) Valid() bool {
	return s == DecodeValidUTF8 || s == DecodeLossyUTF8 || s == DecodeBinary
}

// StreamRecordArgs contains immutable append-only raw output metadata.
type StreamRecordArgs struct {
	RunID         string
	Sequence      int64
	Stream        StreamKind
	ByteOffset    int64
	ByteLength    int64
	RawArtifactID string
	Text          *string
	DecodeStatus  DecodeStatus
	SHA256        string
	ReceivedAt    time.Time
}

// StreamRecordSnapshot is the persistence/API representation of a raw stream record.
type StreamRecordSnapshot struct {
	ID            string       `json:"id"`
	RunID         string       `json:"run_id"`
	Sequence      int64        `json:"sequence"`
	Stream        StreamKind   `json:"stream"`
	ByteOffset    int64        `json:"byte_offset"`
	ByteLength    int64        `json:"byte_length"`
	RawArtifactID string       `json:"raw_artifact_id"`
	Text          *string      `json:"text,omitempty"`
	DecodeStatus  DecodeStatus `json:"decode_status"`
	SHA256        string       `json:"sha256"`
	ReceivedAt    time.Time    `json:"received_at"`
}

// StreamRecord is immutable after construction.
type StreamRecord struct{ snapshot StreamRecordSnapshot }

func NewStreamRecord(args StreamRecordArgs) (*StreamRecord, error) {
	snapshot := StreamRecordSnapshot{
		ID:            NewStreamRecordID(),
		RunID:         args.RunID,
		Sequence:      args.Sequence,
		Stream:        args.Stream,
		ByteOffset:    args.ByteOffset,
		ByteLength:    args.ByteLength,
		RawArtifactID: args.RawArtifactID,
		Text:          cloneOptionalString(args.Text),
		DecodeStatus:  args.DecodeStatus,
		SHA256:        args.SHA256,
		ReceivedAt:    args.ReceivedAt,
	}
	return RestoreStreamRecord(snapshot)
}

func RestoreStreamRecord(snapshot StreamRecordSnapshot) (*StreamRecord, error) {
	if err := validateStreamRecordSnapshot(snapshot); err != nil {
		return nil, err
	}
	copySnapshot := snapshot
	copySnapshot.Text = cloneOptionalString(snapshot.Text)
	copySnapshot.ReceivedAt = snapshot.ReceivedAt.UTC()
	return &StreamRecord{snapshot: copySnapshot}, nil
}

func validateStreamRecordSnapshot(snapshot StreamRecordSnapshot) error {
	if err := validateID(snapshot.ID, streamRecordIDPrefix, "stream_record.id"); err != nil {
		return err
	}
	if err := validateID(snapshot.RunID, runIDPrefix, "run_id"); err != nil {
		return err
	}
	if snapshot.Sequence <= 0 {
		return InvalidRequestf("stream_record.sequence must be positive")
	}
	if !snapshot.Stream.Valid() {
		return InvalidRequestf("invalid stream_record.stream %q", snapshot.Stream)
	}
	if err := requireNonNegative64(snapshot.ByteOffset, "byte_offset"); err != nil {
		return err
	}
	if snapshot.ByteLength <= 0 {
		return InvalidRequestf("byte_length must be positive")
	}
	if err := validateID(snapshot.RawArtifactID, artifactIDPrefix, "raw_artifact_id"); err != nil {
		return err
	}
	if !snapshot.DecodeStatus.Valid() {
		return InvalidRequestf("invalid decode_status %q", snapshot.DecodeStatus)
	}
	if snapshot.DecodeStatus == DecodeValidUTF8 && snapshot.Text == nil {
		return InvalidRequestf("valid_utf8 StreamRecord requires text")
	}
	if snapshot.DecodeStatus == DecodeBinary && snapshot.Text != nil {
		return InvalidRequestf("binary StreamRecord cannot contain text")
	}
	if err := requireSHA256(snapshot.SHA256, "sha256"); err != nil {
		return err
	}
	_, err := normalizeTime(snapshot.ReceivedAt, "received_at")
	return err
}

func (r *StreamRecord) Snapshot() StreamRecordSnapshot {
	out := r.snapshot
	out.Text = cloneOptionalString(r.snapshot.Text)
	return out
}

// ValidateNextStreamRecord enforces per-Run append ordering across stdout and stderr.
func ValidateNextStreamRecord(previous, next StreamRecordSnapshot) error {
	if err := validateStreamRecordSnapshot(previous); err != nil {
		return err
	}
	if err := validateStreamRecordSnapshot(next); err != nil {
		return err
	}
	if previous.RunID != next.RunID {
		return InvalidRequestf("StreamRecord sequence comparison requires the same Run")
	}
	if next.Sequence <= previous.Sequence {
		return InvalidRequestf("StreamRecord sequence must be strictly increasing within a Run")
	}
	return nil
}
