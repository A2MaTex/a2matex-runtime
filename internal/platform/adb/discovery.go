package adb

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/A2MaTex/a2matex-runtime/internal/platform/processrunner"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

const deviceListHeader = "List of devices attached"

type ConnectionState string

const (
	Connected        ConnectionState = "CONNECTED"
	Offline          ConnectionState = "OFFLINE"
	Unauthorized     ConnectionState = "UNAUTHORIZED"
	OtherUnavailable ConnectionState = "OTHER_UNAVAILABLE"
)

type Endpoint struct {
	ADBSerial string
	State     ConnectionState
}

// Discover returns a complete snapshot of ADB connection states. Connected
// endpoints still require a readiness probe before they can be selected.
func (r *Resolver) Discover(ctx context.Context) ([]Endpoint, error) {
	toolchain, err := r.Resolve(ctx)
	if contextErr := ctx.Err(); contextErr != nil {
		return nil, contextErr
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %w", xerror.ErrADBDiscovery, err)
	}

	result := r.run(ctx, toolchain.Executable, []string{"devices", "-l"}, constants.ADBDiscoveryTimeout)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if result.Outcome != processrunner.Succeeded || result.ExitCode == nil || *result.ExitCode != 0 {
		return nil, fmt.Errorf("%w: command did not succeed (%s)", xerror.ErrADBDiscovery, result.Outcome)
	}
	if result.StdoutTruncated {
		return nil, fmt.Errorf("%w: truncated output", xerror.ErrADBDiscovery)
	}
	return parseDeviceList(result.Stdout)
}

func parseDeviceList(stdout []byte) ([]Endpoint, error) {
	if !utf8.Valid(stdout) {
		return nil, fmt.Errorf("%w: invalid UTF-8", xerror.ErrADBDiscovery)
	}

	endpoints := make([]Endpoint, 0)
	seen := make(map[string]struct{})
	headerSeen := false
	for rawLine := range strings.SplitSeq(string(stdout), "\n") {
		line := strings.Trim(strings.TrimSuffix(rawLine, "\r"), " \t")
		if line == "" {
			continue
		}
		if !headerSeen {
			if line != deviceListHeader {
				return nil, fmt.Errorf("%w: missing device list header", xerror.ErrADBDiscovery)
			}
			headerSeen = true
			continue
		}

		serial, rest, ok := splitDeviceField(line)
		if !ok || line == deviceListHeader || strings.ContainsFunc(serial, invalidSerialRune) {
			return nil, fmt.Errorf("%w: malformed device row", xerror.ErrADBDiscovery)
		}
		// "no permissions" starts with "no", which maps to the same
		// unavailable state as other unrecognized ADB states.
		state, _, _ := splitDeviceField(rest)
		if state == "" || strings.ContainsFunc(state, invalidStateRune) {
			return nil, fmt.Errorf("%w: malformed device state", xerror.ErrADBDiscovery)
		}
		if _, exists := seen[serial]; exists {
			return nil, fmt.Errorf("%w: duplicate device serial", xerror.ErrADBDiscovery)
		}
		seen[serial] = struct{}{}
		endpoints = append(endpoints, Endpoint{ADBSerial: serial, State: normalizeState(state)})
	}

	if !headerSeen {
		return nil, fmt.Errorf("%w: missing device list header", xerror.ErrADBDiscovery)
	}
	slices.SortFunc(endpoints, func(a, b Endpoint) int {
		return strings.Compare(a.ADBSerial, b.ADBSerial)
	})
	return endpoints, nil
}

func splitDeviceField(line string) (field, rest string, separated bool) {
	index := strings.IndexAny(line, " \t")
	if index == -1 {
		return line, "", false
	}
	return line[:index], strings.TrimLeft(line[index+1:], " \t"), true
}

func invalidSerialRune(r rune) bool {
	return unicode.IsControl(r) || unicode.IsSpace(r)
}

func invalidStateRune(r rune) bool {
	return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' ||
		r >= '0' && r <= '9' || r == '-' || r == '_')
}

func normalizeState(state string) ConnectionState {
	switch state {
	case "device":
		return Connected
	case "offline":
		return Offline
	case "unauthorized":
		return Unauthorized
	default:
		return OtherUnavailable
	}
}
