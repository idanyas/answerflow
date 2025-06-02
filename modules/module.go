package modules

import (
	"context"

	"answerflow/commontypes"      // ADDED import for shared types
	"answerflow/modules/currency" // ADDED import for currency.APICache
)

// FlowResult, JsonRPCAction, ContextMenuItem definitions are REMOVED from here.
// They are now in 'answerflow/commontypes'.

// Module defines the interface that all Flow Launcher modules must implement.
type Module interface {
	Name() string
	DefaultIconPath() string
	// UPDATED: ProcessQuery now uses currency.APICache and commontypes.FlowResult
	ProcessQuery(ctx context.Context, query string, apiCache *currency.APICache) ([]commontypes.FlowResult, error)
}
