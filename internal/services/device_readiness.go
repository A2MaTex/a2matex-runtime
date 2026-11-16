package services

import (
	"context"

	"golang.org/x/sync/errgroup"

	"github.com/A2MaTex/a2matex-runtime/internal/platform/adb"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

func evaluateDeviceReadiness(ctx context.Context, source DeviceInventorySource, endpoints []adb.Endpoint, limit int) ([]adb.Readiness, error) {
	devices := make([]adb.Readiness, len(endpoints))
	group, probeCtx := errgroup.WithContext(ctx)
	group.SetLimit(limit)
	for index, endpoint := range endpoints {
		if probeCtx.Err() != nil {
			break
		}
		group.Go(func() error {
			if err := probeCtx.Err(); err != nil {
				return xerror.WithOperation("probe_device_readiness", err)
			}
			result, err := source.ProbeReadiness(probeCtx, endpoint)
			if err != nil {
				return xerror.WithOperation("probe_device_readiness", err)
			}
			devices[index] = result
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, xerror.WithOperation("evaluate_device_readiness", err)
	}

	return devices, nil
}
