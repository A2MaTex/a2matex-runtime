package execution

import (
	"regexp"
	"strings"

	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	"github.com/google/uuid"
)

var entryNodeID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

func ValidNodeID(id string) bool { return entryNodeID.MatchString(id) }

// ValidID reports whether id is a canonical Runtime UUID v4.
func ValidID(id string) bool {
	parsed, err := uuid.Parse(id)
	return err == nil &&
		parsed.Version() == 4 &&
		parsed.Variant() == uuid.RFC4122 &&
		parsed.String() == id
}

// ValidNewAdmission checks in-memory inputs for a new run without accessing SQLite.
func ValidNewAdmission(admission Admission) bool {
	if !ValidID(admission.RunGroupID) || admission.Now.IsZero() ||
		len(admission.Devices) == 0 || len(admission.Devices) > constants.MaxAdmissionDevices {
		return false
	}
	ids, serials := make(map[string]bool), make(map[string]bool)
	for _, device := range admission.Devices {
		if !ValidID(device.DeviceExecutionID) || !ValidNodeID(device.EntryNodeID) ||
			strings.TrimSpace(device.ADBSerial) == "" || ids[device.DeviceExecutionID] || serials[device.ADBSerial] {
			return false
		}
		ids[device.DeviceExecutionID], serials[device.ADBSerial] = true, true
	}
	return true
}
