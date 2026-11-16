package sqlitestore

import (
	"errors"
	"strconv"

	"modernc.org/sqlite"

	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

// ErrorDetail reports an extended SQLite result code without rendering the driver error.
func ErrorDetail(cause error) string {
	var driverError *sqlite.Error
	if errors.As(cause, &driverError) {
		return "sqlite_code=" + strconv.Itoa(driverError.Code())
	}
	return ""
}

func storeFailure(operation, message string, cause error) error {
	detail := message
	if code := ErrorDetail(cause); code != "" {
		detail += " (" + code + ")"
	}
	return &xerror.OperationError{Operation: operation, Detail: detail, Err: errors.New(message)}
}
