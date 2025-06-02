package commontypes

// FlowResult represents a single item in the list of results for Flow Launcher.
type FlowResult struct {
	Title            string            `json:"Title"`
	SubTitle         string            `json:"SubTitle"`
	IcoPath          string            `json:"IcoPath,omitempty"`
	Score            int               `json:"Score"`
	JsonRPCAction    JsonRPCAction     `json:"JsonRPCAction"`
	ContextMenuItems []ContextMenuItem `json:"ContextMenuItems,omitempty"`
}

// JsonRPCAction defines an action to be performed by Flow Launcher.
type JsonRPCAction struct {
	Method     string        `json:"method"`
	Parameters []interface{} `json:"parameters"`
}

// ContextMenuItem defines an item in the context menu for a FlowResult.
type ContextMenuItem struct {
	Title         string        `json:"Title"`
	SubTitle      string        `json:"SubTitle"`
	IcoPath       string        `json:"IcoPath,omitempty"`
	JsonRPCAction JsonRPCAction `json:"JsonRPCAction"`
}
