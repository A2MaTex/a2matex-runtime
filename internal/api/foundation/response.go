package foundation

type healthResponse struct {
	Status string `json:"status"`
}

type supportedNode struct {
	Type           string `json:"type"`
	RuntimeHandler string `json:"runtimeHandler"`
}

type runtimeInfoResponse struct {
	RuntimeVersion          string          `json:"runtimeVersion"`
	RuntimeIRFormatVersions []int           `json:"runtimeIrFormatVersions"`
	SupportedNodes          []supportedNode `json:"supportedNodes"`
}
