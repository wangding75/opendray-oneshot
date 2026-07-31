package domain

import (
	"crypto/rand"
	"encoding/base32"
	"strings"
)

const idEntropyBytes = 14

const (
	taskIDPrefix             = "otk_"
	deliveryIDPrefix         = "odl_"
	runIDPrefix              = "orn_"
	runtimeContextIDPrefix   = "orc_"
	streamRecordIDPrefix     = "osr_"
	standardEventIDPrefix    = "ose_"
	artifactIDPrefix         = "oar_"
	stagedAttachmentIDPrefix = "oat_"
)

func newID(prefix string) string {
	var entropy [idEntropyBytes]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		panic("oneshot/domain: crypto/rand unavailable: " + err.Error())
	}
	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(entropy[:])
	if len(encoded) > 22 {
		encoded = encoded[:22]
	}
	return prefix + strings.ToLower(encoded)
}

func validateID(id, prefix, field string) error {
	if !strings.HasPrefix(id, prefix) || len(id) <= len(prefix) {
		return InvalidRequestf("%s must use %s prefix", field, prefix)
	}
	for _, r := range id[len(prefix):] {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return InvalidRequestf("%s contains invalid characters", field)
		}
	}
	return nil
}

// NewTaskID returns an opaque Task identifier.
func NewTaskID() string { return newID(taskIDPrefix) }

// NewDeliveryID returns an opaque execution Delivery identifier.
func NewDeliveryID() string { return newID(deliveryIDPrefix) }

// NewRunID returns an opaque Run identifier.
func NewRunID() string { return newID(runIDPrefix) }

// NewRuntimeContextID returns an opaque RuntimeContext identifier.
func NewRuntimeContextID() string { return newID(runtimeContextIDPrefix) }

// NewStreamRecordID returns an opaque raw stream record identifier.
func NewStreamRecordID() string { return newID(streamRecordIDPrefix) }

// NewStandardEventID returns an opaque normalized event identifier.
func NewStandardEventID() string { return newID(standardEventIDPrefix) }

// NewArtifactID returns an opaque Artifact identifier.
func NewArtifactID() string { return newID(artifactIDPrefix) }

// NewStagedAttachmentID returns an opaque pre-execution attachment identifier.
func NewStagedAttachmentID() string { return newID(stagedAttachmentIDPrefix) }

// ValidateStagedAttachmentID validates the public opaque attachment reference.
func ValidateStagedAttachmentID(id string) error {
	return validateID(id, stagedAttachmentIDPrefix, "staged_attachment.id")
}

func requireNonEmpty(value, field string) error {
	if strings.TrimSpace(value) == "" {
		return InvalidRequestf("%s is required", field)
	}
	return nil
}

func requirePositive(value int, field string) error {
	if value <= 0 {
		return InvalidRequestf("%s must be positive", field)
	}
	return nil
}

func requireNonNegative64(value int64, field string) error {
	if value < 0 {
		return InvalidRequestf("%s must not be negative", field)
	}
	return nil
}

func requireSHA256(value, field string) error {
	if len(value) != 64 {
		return InvalidRequestf("%s must be a lowercase hexadecimal SHA-256 digest", field)
	}
	for _, r := range value {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return InvalidRequestf("%s must be a lowercase hexadecimal SHA-256 digest", field)
		}
	}
	return nil
}
