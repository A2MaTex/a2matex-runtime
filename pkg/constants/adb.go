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
	DeviceNotFound     = "DEVICE_NOT_FOUND"
	DeviceUnauthorized = "DEVICE_UNAUTHORIZED"
	DeviceOffline      = "DEVICE_OFFLINE"
	DeviceNotReady     = "DEVICE_NOT_READY"
)
