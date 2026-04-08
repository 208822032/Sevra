package tokenizer

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"net/http"

	"github.com/sashabaranov/go-openai"
)

type tokenInfo struct {
	Code  int    `json:"code"`
	Count int    `json:"num_tokens"`
	Msg   string `json:"msg"`
}

func GetTokenCount(message openai.ChatCompletionMessage, model string) (int, error) {
	url := fmt.Sprintf("http://192.168.239.161:3002/tokenizer/%s", model)
	info := tokenInfo{}
	if err := postJSON(url, &message, &info); err != nil {
		return estimateTokenCount(message), nil
	}
	if info.Code != 200 {
		return estimateTokenCount(message), nil
	}
	return info.Count, nil
}

func estimateTokenCount(message openai.ChatCompletionMessage) int {
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

func postJSON(url string, requestData *openai.ChatCompletionMessage, responseData *tokenInfo) error {
	requestBody, err := json.Marshal(requestData)
	if err != nil {
		return err
	}

	resp, err := http.Post(url, "application/json", bytes.NewBuffer(requestBody))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	return json.NewDecoder(resp.Body).Decode(responseData)
}
