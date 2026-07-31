package delivery

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"
)

var ErrOutboxNotFound = errors.New("channel delivery outbox record not found")

// MemoryOutboxStore is used by focused tests and nil-database deployments. It
// implements the same lease/retry semantics as the PostgreSQL store.
type MemoryOutboxStore struct {
	mu       sync.Mutex
	records  map[string]OutboxRecord
	byKey    map[string]string
	attempts map[string][]ChannelDeliveryAttempt
	now      func() time.Time
}

func NewMemoryOutboxStore() *MemoryOutboxStore {
	return &MemoryOutboxStore{
		records:  make(map[string]OutboxRecord),
		byKey:    make(map[string]string),
		attempts: make(map[string][]ChannelDeliveryAttempt),
		now:      time.Now,
	}
}

func (s *MemoryOutboxStore) Create(_ context.Context, record OutboxRecord) (OutboxRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id := s.byKey[record.IdempotencyKey]; id != "" {
		return s.records[id], false, nil
	}
	now := s.now().UTC()
	if record.CreatedAt.IsZero() {
		record.CreatedAt = now
	}
	if record.UpdatedAt.IsZero() {
		record.UpdatedAt = now
	}
	if record.NextAttemptAt.IsZero() {
		record.NextAttemptAt = now
	}
	if record.Status == "" {
		record.Status = StatusPending
	}
	s.records[record.ID] = cloneRecord(record)
	s.byKey[record.IdempotencyKey] = record.ID
	return cloneRecord(record), true, nil
}

func (s *MemoryOutboxStore) Get(_ context.Context, id string) (OutboxRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[id]
	if !ok {
		return OutboxRecord{}, ErrOutboxNotFound
	}
	return cloneRecord(record), nil
}

func (s *MemoryOutboxStore) Claim(_ context.Context, id, owner string, lease time.Duration) (OutboxRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	record, ok := s.records[id]
	if !ok {
		return OutboxRecord{}, false, ErrOutboxNotFound
	}
	if !claimable(record, now) {
		return cloneRecord(record), false, nil
	}
	record.Status = StatusSending
	record.LeaseOwner = owner
	record.LeaseUntil = now.Add(lease)
	record.UpdatedAt = now
	record.AttemptCount++
	s.records[id] = record
	return cloneRecord(record), true, nil
}

func (s *MemoryOutboxStore) ClaimDue(_ context.Context, owner string, limit int, lease time.Duration) ([]OutboxRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit <= 0 {
		limit = 32
	}
	now := s.now().UTC()
	ids := make([]string, 0, len(s.records))
	for id, record := range s.records {
		if claimable(record, now) {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool {
		return s.records[ids[i]].CreatedAt.Before(s.records[ids[j]].CreatedAt)
	})
	if len(ids) > limit {
		ids = ids[:limit]
	}
	out := make([]OutboxRecord, 0, len(ids))
	for _, id := range ids {
		record := s.records[id]
		record.Status = StatusSending
		record.LeaseOwner = owner
		record.LeaseUntil = now.Add(lease)
		record.UpdatedAt = now
		record.AttemptCount++
		s.records[id] = record
		out = append(out, cloneRecord(record))
	}
	return out, nil
}

func claimable(record OutboxRecord, now time.Time) bool {
	if record.Status == StatusDelivered || record.Status == StatusDead {
		return false
	}
	if record.Status == StatusSending && record.LeaseUntil.After(now) {
		return false
	}
	return !record.NextAttemptAt.After(now)
}

func (s *MemoryOutboxStore) AppendAttempt(_ context.Context, attempt ChannelDeliveryAttempt) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attempts[attempt.DeliveryID] = append(s.attempts[attempt.DeliveryID], attempt)
	return nil
}

func (s *MemoryOutboxStore) MarkProgress(_ context.Context, id, owner string, progress int, receipt DeliveryReceipt) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[id]
	if !ok {
		return ErrOutboxNotFound
	}
	if record.LeaseOwner != owner {
		return errors.New("channel delivery lease owner mismatch")
	}
	record.Progress = progress
	record.Receipt = receipt
	record.UpdatedAt = s.now().UTC()
	s.records[id] = cloneRecord(record)
	return nil
}

func (s *MemoryOutboxStore) MarkDelivered(_ context.Context, id, owner string, receipt DeliveryReceipt) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[id]
	if !ok {
		return ErrOutboxNotFound
	}
	if record.LeaseOwner != owner {
		return errors.New("channel delivery lease owner mismatch")
	}
	now := s.now().UTC()
	record.Status = StatusDelivered
	record.Progress = receipt.CompletedParts
	record.Receipt = receipt
	record.DeliveredAt = now
	record.UpdatedAt = now
	record.LeaseOwner = ""
	record.LeaseUntil = time.Time{}
	record.LastError = ""
	s.records[id] = cloneRecord(record)
	return nil
}

func (s *MemoryOutboxStore) MarkRetry(_ context.Context, id, owner, lastError string, nextAttempt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[id]
	if !ok {
		return ErrOutboxNotFound
	}
	if record.LeaseOwner != owner {
		return errors.New("channel delivery lease owner mismatch")
	}
	record.Status = StatusRetry
	record.LastError = lastError
	record.NextAttemptAt = nextAttempt
	record.UpdatedAt = s.now().UTC()
	record.LeaseOwner = ""
	record.LeaseUntil = time.Time{}
	s.records[id] = cloneRecord(record)
	return nil
}

func (s *MemoryOutboxStore) MarkDead(_ context.Context, id, owner, lastError string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[id]
	if !ok {
		return ErrOutboxNotFound
	}
	if record.LeaseOwner != owner {
		return errors.New("channel delivery lease owner mismatch")
	}
	record.Status = StatusDead
	record.LastError = lastError
	record.UpdatedAt = s.now().UTC()
	record.LeaseOwner = ""
	record.LeaseUntil = time.Time{}
	s.records[id] = cloneRecord(record)
	return nil
}

func (s *MemoryOutboxStore) Attempts(id string) []ChannelDeliveryAttempt {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ChannelDeliveryAttempt, len(s.attempts[id]))
	copy(out, s.attempts[id])
	return out
}

func cloneRecord(record OutboxRecord) OutboxRecord {
	record.Payload = append([]byte(nil), record.Payload...)
	record.Receipt.MessageIDs = append([]string(nil), record.Receipt.MessageIDs...)
	return record
}
