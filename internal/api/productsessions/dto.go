package productsessions

import (
	"github.com/A2MaTex/a2matex-runtime/internal/services/productsession"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
)

type PackageSelection struct {
	PackageRef      string `json:"packageRef"`
	PackageHash     string `json:"packageHash"`
	LauncherVersion string `json:"launcherVersion"`
}

type PrepareRequest struct {
	RequestID  string                          `json:"requestId"`
	Purpose    constants.ProductSessionPurpose `json:"purpose"`
	RunGroupID *string                         `json:"runGroupId,omitempty"`
	Package    PackageSelection                `json:"package"`
}

// Prepared contains exactly the approved public JSON fields; Replayed is hidden.
type PreparedSessionResponse = productsession.Prepared
