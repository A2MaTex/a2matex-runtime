package services

import (
	"context"

	"golang.org/x/sync/errgroup"

	"github.com/A2MaTex/a2matex-runtime/internal/platform/adb"
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
				return err
			}
			result, err := source.ProbeReadiness(probeCtx, endpoint)
			if err != nil {
				return err
			}
			devices[index] = result
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	return devices, nil
}
