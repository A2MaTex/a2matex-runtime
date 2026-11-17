package constants

const (
	RuntimeIRMaxNodes         = 500
	RuntimeIRMaxEdges         = 1_000
	RuntimeIRMaxFields        = 100
	RunRequestMaxContentBytes = 4 * 1024 * 1024
	MaxWorkflowStringBound    = 10_000
	MaxSafeInteger            = 9_007_199_254_740_991
	MaxSwitchRules            = 32
	RuntimeIdentifierPattern  = `^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`
	WorkflowFieldNamePattern  = `^[a-z][a-zA-Z0-9]{0,99}$`
	VariableNamePattern       = `^[A-Za-z_][A-Za-z0-9_]{0,99}$`
	RuntimeCodePattern        = `^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$`
	RuntimeVersionPattern     = `^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`
	RuntimeCodeMaxLength      = 255
	ValidationFailed          = "VALIDATION_FAILED"
	RuntimeIncompatible       = "RUNTIME_INCOMPATIBLE"
	InputSourceLiteral        = "LITERAL"
	InputSourceWorkflowInput  = "WORKFLOW_INPUT"
	InputSourceNodeOutput     = "NODE_OUTPUT"
	InputSourceFlowVariable   = "FLOW_VARIABLE"
	InputSourceGlobalVariable = "GLOBAL_VARIABLE"
	ErrorPolicyStop           = "STOP"
	ErrorPolicyErrorBranch    = "ERROR_BRANCH"
)
