package productpackages

import (
	"encoding/json"

	packagevalidation "github.com/A2MaTex/a2matex-runtime/internal/validation/productpackage"
)

type InputSchemaRequest struct {
	PackageRef      string `json:"packageRef"`
	LauncherVersion string `json:"launcherVersion"`
}

type InputSchemaResponse struct {
	Package     packagevalidation.PublicPackage `json:"package"`
	InputSchema json.RawMessage                 `json:"inputSchema"`
}
