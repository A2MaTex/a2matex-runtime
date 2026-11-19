package constants

type VariableScope string

const (
	VariableScopeFlow     VariableScope = "FLOW"
	VariableScopeRunGroup VariableScope = "RUN_GROUP"
	MaxVariableValueBytes               = 64 * 1024
	MaxVariablesPerScope                = 512
	MaxVariableScopeBytes               = 1024 * 1024
)
