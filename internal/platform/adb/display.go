package adb

import (
	"context"
	"strconv"
	"strings"

	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

type DisplaySize struct {
	Width, Height int
}

// DefaultDisplaySize reads the current logical size rather than the base size.
func (d *Dispatcher) DefaultDisplaySize(ctx context.Context, serial string) (DisplaySize, error) {
	result, err := d.runDevice(ctx, serial, []string{"shell", "-T", "dumpsys", "window", "displays"}, constants.ProcessOutputLimitBytes, 0)
	if err != nil {
		return DisplaySize{}, err
	}
	if err := ctx.Err(); err != nil {
		return DisplaySize{}, err
	}
	if !result.StdoutTruncated {
		if size, ok := parseDefaultDisplaySize(string(result.Stdout)); ok {
			return size, nil
		}
	}
	return DisplaySize{}, xerror.CommandError{Code: constants.CommandFailed, Message: "cannot determine the current default display size"}
}

func parseDefaultDisplaySize(output string) (DisplaySize, bool) {
	var size DisplaySize
	inDefault, seenDefault := false, false
	for line := range strings.SplitSeq(output, "\n") {
		line = strings.TrimSpace(line)
		if header, ok := strings.CutPrefix(line, "Display: mDisplayId="); ok {
			fields := strings.Fields(header)
			inDefault = len(fields) > 0 && fields[0] == "0"
			if inDefault && seenDefault {
				return DisplaySize{}, false
			}
			seenDefault = seenDefault || inDefault
			continue
		}
		if !inDefault || (!strings.HasPrefix(line, "init=") && !strings.HasPrefix(line, "cur=")) {
			continue
		}
		for _, field := range strings.Fields(line) {
			dimensions, ok := strings.CutPrefix(field, "cur=")
			if !ok {
				continue
			}
			widthText, heightText, _ := strings.Cut(dimensions, "x")
			width, widthErr := strconv.ParseUint(widthText, 10, 31)
			height, heightErr := strconv.ParseUint(heightText, 10, 31)
			if size.Width != 0 || widthErr != nil || heightErr != nil || width == 0 || height == 0 {
				return DisplaySize{}, false
			}
			size = DisplaySize{Width: int(width), Height: int(height)}
		}
	}
	return size, size.Width != 0
}
