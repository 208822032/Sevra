package server

import (
	"context"

	"ai-chat-service/proto"
)

// checkSensitive is intentionally separate from retrieval.  Chat safety must
// remain available while the old QA implementation is removed.
func (a *app) checkSensitive(ctx context.Context, in *proto.ChatCompletionRequest) (bool, string, error) {
	if a == nil {
		return true, "", nil
	}
	if a.sensitiveFn != nil {
		return a.sensitiveFn(ctx, in)
	}
	return a.sensitive(ctx, in)
}
