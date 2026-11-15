package xerror

import "errors"

var (
	ErrADBDiscovery        = errors.New("ADB device discovery failed")
	ErrADBNotFound         = errors.New("ADB executable not found")
	ErrADBInvalidCandidate = errors.New("ADB executable candidate is unusable")
	ErrADBInstallLocation  = errors.New("cannot resolve the Runtime installation location")
	ErrADBVersionProbe     = errors.New("ADB version probe failed")
)
