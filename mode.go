package main

import (
	"github.com/crossplane/function-sdk-go/errors"
)

// Mode is a lifecycle mode. Each mode maps to a set of management policies
// that crossplane-runtime supports.
type Mode string

// Supported lifecycle modes.
const (
	// ModeFull lets Crossplane manage the entire lifecycle, including deletion.
	ModeFull Mode = "Full"

	// ModeProvision creates, imports, and updates resources but never deletes
	// them. Deleting the XR orphans the external resources.
	ModeProvision Mode = "Provision"

	// ModeObserveOnly imports and watches resources without changing them.
	ModeObserveOnly Mode = "ObserveOnly"

	// ModeDeprovision imports and deletes resources without reconciling drift.
	ModeDeprovision Mode = "Deprovision"
)

// Policies returns the management policies for the mode. Every set is one of
// the combinations crossplane-runtime supports.
func (m Mode) Policies() ([]string, error) {
	switch m {
	case ModeFull:
		return []string{"*"}, nil
	case ModeProvision:
		return []string{"Observe", "Create", "Update", "LateInitialize"}, nil
	case ModeObserveOnly:
		return []string{"Observe"}, nil
	case ModeDeprovision:
		return []string{"Observe", "Create", "Delete"}, nil
	default:
		return nil, errors.Errorf("unknown mode %q: must be one of Full, Provision, ObserveOnly, Deprovision", m)
	}
}
