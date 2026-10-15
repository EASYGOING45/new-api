package cursor

import (
	"errors"
	"io"

	native "github.com/QuantumNous/new-api/pkg/cursor"
	"github.com/QuantumNous/new-api/relay/channel/openai"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
)

// Adaptor keeps the experimental Cursor transport separate from host routing,
// account selection and settlement. Responses use the existing OpenAI handlers.
type Adaptor struct{ openai.Adaptor }

func (a *Adaptor) ConvertOpenAIRequest(_ *gin.Context, info *relaycommon.RelayInfo, r *dto.GeneralOpenAIRequest) (any, error) {
	if info.RelayMode != relayconstant.RelayModeChatCompletions || info.RelayFormat != types.RelayFormatOpenAI {
		return nil, errors.New("Cursor native channel supports OpenAI Chat Completions only")
	}
	return r, nil
}
func (a *Adaptor) DoRequest(c *gin.Context, info *relaycommon.RelayInfo, body io.Reader) (any, error) {
	if info.ChannelBaseUrl != "" && info.ChannelBaseUrl != "https://api2.cursor.sh" {
		return nil, errors.New("Cursor native channel requires the fixed Cursor service address")
	}
	if info.RelayMode != relayconstant.RelayModeChatCompletions || info.RelayFormat != types.RelayFormatOpenAI {
		return nil, errors.New("Cursor native channel supports OpenAI Chat Completions only")
	}
	b, err := io.ReadAll(io.LimitReader(body, (2<<20)+1))
	if err != nil || len(b) > 2<<20 {
		return nil, errors.New("Cursor request exceeds the native demo limit")
	}
	return native.Default.Chat(c.Request.Context(), info.ApiKey, b), nil
}
func (a *Adaptor) GetModelList() []string { return nil }
func (a *Adaptor) GetChannelName() string { return "cursor" }
