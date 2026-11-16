package main

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"strconv"

	"github.com/A2MaTex/a2matex-runtime/internal/platform/httpserver"
	"github.com/A2MaTex/a2matex-runtime/internal/supervisor"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

func logRuntimeFailure(ctx context.Context, logger *slog.Logger, stage, message string, err error) {
	attrs := []slog.Attr{
		slog.String("component", "runtime"),
		slog.String("event", "runtime_failed"),
	}
	failures := splitRuntimeFailures(err)
	attrs = append(attrs, runtimeFailureAttributes(stage, failures[0])...)
	if len(failures) > 1 {
		related := make([]slog.Attr, 0, len(failures)-1)
		for index, failure := range failures[1:] {
			related = append(related, slog.Attr{
				Key: strconv.Itoa(index + 1), Value: slog.GroupValue(runtimeFailureAttributes(stage, failure)...),
			})
		}
		attrs = append(attrs, slog.Attr{Key: "related_failures", Value: slog.GroupValue(related...)})
	}
	logger.LogAttrs(ctx, slog.LevelError, message, attrs...)
}

// Joined errors are ordered by the supervisor with the primary failure first.
func splitRuntimeFailures(err error) []error {
	joined, ok := err.(interface{ Unwrap() []error })
	if !ok {
		return []error{err}
	}
	var failures []error
	for _, cause := range joined.Unwrap() {
		failures = append(failures, splitRuntimeFailures(cause)...)
	}
	if len(failures) == 0 {
		return []error{err}
	}
	return failures
}

func runtimeFailureAttributes(stage string, err error) []slog.Attr {
	var attrs []slog.Attr
	var lifecycle *supervisor.LifecycleError
	if errors.As(err, &lifecycle) {
		stage = lifecycle.Operation
		if lifecycle.Component != "" {
			attrs = append(attrs, slog.String("failed_component", lifecycle.Component))
		}
	}
	attrs = append(attrs, slog.String("stage", stage))
	var operation *xerror.OperationError
	if errors.As(err, &operation) {
		attrs = append(attrs, slog.String("operation", operation.Operation))
		if operation.Detail != "" {
			attrs = append(attrs, slog.String("reason", operation.Detail))
		}
	}
	if classification := runtimeFailureCode(err); classification != "" {
		attrs = append(attrs, slog.String("error_code", classification))
	}
	return attrs
}

func runtimeFailureCode(err error) string {
	if code := xerror.ExecutionFailureCode(err); code != "" {
		return code
	}
	switch {
	case errors.Is(err, xerror.ErrExecutionStoreInUse):
		return "EXECUTION_STORE_IN_USE"
	case errors.Is(err, httpserver.ErrAddressInUse):
		return "ADDRESS_IN_USE"
	case errors.Is(err, fs.ErrNotExist):
		return "FILE_NOT_FOUND"
	case errors.Is(err, fs.ErrPermission):
		return "PERMISSION_DENIED"
	case errors.Is(err, context.DeadlineExceeded):
		return "DEADLINE_EXCEEDED"
	case errors.Is(err, context.Canceled):
		return "CANCELLED"
	case errors.Is(err, xerror.ErrGenerateLocalAuthSession):
		return "SESSION_GENERATION_FAILED"
	case errors.Is(err, xerror.ErrResolveLocalAuthDirectory):
		return "CONNECTION_DIRECTORY_UNAVAILABLE"
	case errors.Is(err, xerror.ErrPrepareLocalAuthDirectory):
		return "CONNECTION_DIRECTORY_PREPARATION_FAILED"
	case errors.Is(err, xerror.ErrPublishLocalAuthConnection):
		return "CONNECTION_PUBLICATION_FAILED"
	case errors.Is(err, xerror.ErrCleanupLocalAuthConnection):
		return "CONNECTION_CLEANUP_FAILED"
	default:
		return ""
	}
}
