package xerror

import "errors"

var (
	ErrEncodeLocalAuthConnection  = errors.New("encode local authentication connection document")
	ErrResolveLocalAuthDirectory  = errors.New("resolve local authentication directory")
	ErrPrepareLocalAuthDirectory  = errors.New("prepare local authentication directory")
	ErrPublishLocalAuthConnection = errors.New("publish local authentication connection document")
	ErrCleanupLocalAuthConnection = errors.New("clean up local authentication connection document")
)
