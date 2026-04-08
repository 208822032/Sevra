package tokenizer

import (
	"ai-chat-service/pkg/config"
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"net/http"

	"github.com/sashabaranov/go-openai"
)

type tokensInfo struct {
	Code   int    `json:"code"`
	Tokens int    `json:"num_tokens"`
	Msg    string `json:"msg"`
}

var httpClient = &http.Client{}

func GetTokens(message *openai.ChatCompletionMessage, model string) (int, error) {
	count, _, err := GetTokensWithSource(message, model)
	return count, err
}

func GetTokensWithSource(message *openai.ChatCompletionMessage, model string) (int, string, error) {
	cnf := config.GetConfig()
	url := fmt.Sprintf("%s/tokenizer/%s", cnf.DependOn.Tokenizer.Address, model)
	info := &tokensInfo{}
	if err := postJSON(url, message, info); err != nil {
		return estimateTokenCount(message), "estimated", nil
	}
	if info.Code != 200 {
		return estimateTokenCount(message), "estimated", nil
	}
	return info.Tokens, "provider", nil
}

func estimateTokenCount(message *openai.ChatCompletionMessage) int {
	if message == nil {
		return 0
	}
	contentLength := len([]rune(message.Content))
	if contentLength == 0 {
		return 8
	}
	estimated := int(math.Ceil(float64(contentLength)/4.0)) + 8
	if estimated < 8 {
		return 8
	}
	return estimated
}

func postJSON(url string, requestData *openai.ChatCompletionMessage, responseData *tokensInfo) error {
	requestBody, err := json.Marshal(requestData)
	if err != nil {
		return err
	}
	resp, err := httpClient.Post(url, "application/json", bytes.NewReader(requestBody))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(responseData)
}
