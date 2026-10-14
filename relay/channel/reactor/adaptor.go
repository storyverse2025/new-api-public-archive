package reactor

import (
	"fmt"

	"github.com/QuantumNous/new-api/relay/channel/openai"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
)

// Adaptor supplies channel discovery while rejecting synchronous endpoints.
// Fast H3 Omni only runs through TaskAdaptor and the private official-SDK worker.
// It does not support chat streams or StreamOptions.
type Adaptor struct{ openai.Adaptor }

func (a *Adaptor) GetRequestURL(*relaycommon.RelayInfo) (string, error) {
	return "", fmt.Errorf("reactor: use the asynchronous /v1/videos endpoint")
}
func (a *Adaptor) GetModelList() []string { return ModelList }
func (a *Adaptor) GetChannelName() string { return "Reactor" }
