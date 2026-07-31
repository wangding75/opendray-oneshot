package domain

import "fmt"

// PrincipalKind is the closed One-shot owner kind set.
type PrincipalKind string

const (
	PrincipalAdmin       PrincipalKind = "admin"
	PrincipalIntegration PrincipalKind = "integration"
)

func (k PrincipalKind) String() string { return string(k) }

// Valid reports whether the kind is frozen in the One-shot contract.
func (k PrincipalKind) Valid() bool {
	switch k {
	case PrincipalAdmin, PrincipalIntegration:
		return true
	default:
		return false
	}
}

// Owner is the immutable principal identity used by Task, Delivery and RuntimeContext.
type Owner struct {
	Kind PrincipalKind `json:"principal_kind"`
	ID   string        `json:"principal_id"`
}

func (o Owner) Validate() error {
	if !o.Kind.Valid() {
		return InvalidRequestf("invalid principal_kind %q", o.Kind)
	}
	if err := requireNonEmpty(o.ID, "principal_id"); err != nil {
		return err
	}
	return nil
}

func (o Owner) Equal(other Owner) bool { return o.Kind == other.Kind && o.ID == other.ID }

func (o Owner) String() string { return fmt.Sprintf("%s:%s", o.Kind, o.ID) }
