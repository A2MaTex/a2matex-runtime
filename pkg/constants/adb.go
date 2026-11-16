package constants

import "time"

const (
	ADBVersionProbeTimeout = 3 * time.Second
	ADBDiscoveryTimeout    = 5 * time.Second
)

const (
	ADBReadinessTimeout           = 5 * time.Second
	DeviceInventoryRefreshTimeout = 20 * time.Second
	DeviceInventoryProbeLimit     = 4
	DevicePreflightTimeout        = 20 * time.Second
	DevicePreflightProbeLimit     = 4
)

const (
	MaxAndroidPixelCoordinate = 100_000
	MaxAndroidScreenPercent   = 100
	AndroidHierarchyMaxBytes  = 1 << 20
	AndroidHierarchyCleanup   = time.Second

	AndroidTargetPixels   = "ABSOLUTE_PIXELS"
	AndroidTargetPercent  = "SCREEN_PERCENT"
	AndroidTargetSelector = "SELECTOR_ID"

	ADBUnavailable          = "ADB_UNAVAILABLE"
	CommandInputUnsupported = "COMMAND_INPUT_UNSUPPORTED"
	CommandStartFailed      = "COMMAND_START_FAILED"
	CommandFailed           = "COMMAND_FAILED"
)

// AndroidSelectorIDPattern follows the Backend's ECMAScript whitespace rule.
const AndroidSelectorIDPattern = `^[^` + androidSelectorSpace + `:]+:id/[^/` + androidSelectorSpace + `]+$`
const androidSelectorSpace = `\x09-\x0D\x20\x{00A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}\x{FEFF}`

const (
	DeviceNotFound     = "DEVICE_NOT_FOUND"
	DeviceUnauthorized = "DEVICE_UNAUTHORIZED"
	DeviceOffline      = "DEVICE_OFFLINE"
	DeviceNotReady     = "DEVICE_NOT_READY"
)
