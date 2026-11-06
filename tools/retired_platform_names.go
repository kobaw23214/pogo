package tools

// Retired platform verbs remain reserved so saved grants cannot be rebound
// to an unrelated Skill or MCP implementation. They are never registered.
const (
	WorkflowDiscoverName = "workflow_discover"
	WorkflowReadName     = "workflow_read"
	WorkflowValidateName = "workflow_validate"
	WorkflowEditName     = "workflow_edit"
	WorkflowTestName     = "workflow_test"
	WorkflowRunName      = "workflow_run"
	WorkflowPublishName  = "workflow_publish"
)
