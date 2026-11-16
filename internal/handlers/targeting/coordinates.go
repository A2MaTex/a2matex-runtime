package targeting

import (
	"encoding/json"
	"math"

	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
)

// ReadInteger accepts JSON numbers that represent integers within the bounds.
func ReadInteger(raw json.RawMessage, minimum, maximum int) (int, bool) {
	var value *float64
	if json.Unmarshal(raw, &value) != nil || value == nil ||
		*value < float64(minimum) || *value > float64(maximum) || math.Trunc(*value) != *value {
		return 0, false
	}
	return int(*value), true
}

// PercentPixel converts a validated percentage on a positive display axis.
func PercentPixel(percent, dimension int) int {
	const scale = constants.MaxAndroidScreenPercent
	return int((int64(percent)*int64(dimension-1) + scale/2) / scale)
}
