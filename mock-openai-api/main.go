package main

import (
	bytes2 "bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math"
	"math/rand"
	"mock-openai-api/middleware"
	"mock-openai-api/pkg/config"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/sashabaranov/go-openai"
)

var (
	configFile = flag.String("config", "dev.config.yaml", "")
)

func main() {
	flag.Parse()
	config.InitConfig(*configFile)
	cnf := config.GetConfig()
	gin.SetMode(cnf.Http.Mode)
	r := gin.Default()
	r.GET("/health", func(c *gin.Context) {})

	r.Use(middleware.Auth())
	v1 := r.Group("/v1")
	v1.POST("/chat/completions", chatCompletion)
	v1.POST("/embeddings", embeddings)
	err := r.Run(fmt.Sprintf("%s:%d", cnf.Http.Host, cnf.Http.Port))
	if err != nil {
		log.Println(err)
	}
}

var msgList = []string{
	"你好，有什么可以帮助你的吗？",
	"今天的天气是真的很不错，你应该好好的放松一下了？",
	"希望你学习golang，能够找到一份心仪的工作？",
}

func chatCompletion(ctx *gin.Context) {
	req := openai.ChatCompletionRequest{}
	ctx.ShouldBindJSON(&req)

	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	index := r.Intn(len(msgList))
	msg := msgList[index]
	if !req.Stream {
		res := buildChatCompletionResponse(msg)
		ctx.JSON(http.StatusOK, res)
		return
	}
	list := buildChatCompletionStreamResponseList(msg)
	ctx.Header("Content-type", "application/octet-stream")
	for i := 0; i < len(list); i++ {
		item := list[i]
		if i != 0 {
			ctx.Writer.Write([]byte("\n"))
		}
		var buffer bytes2.Buffer
		buffer.WriteString("data: ")
		bytes, err := json.Marshal(item)
		if err != nil {
			fmt.Println(err)
			break
		}
		buffer.Write(bytes)
		ctx.Writer.Write(buffer.Bytes())
		ctx.Writer.Flush()
		time.Sleep(time.Millisecond * 100)
	}
}

type embeddingWeight struct {
	index  int
	weight float32
}

func weights(values ...embeddingWeight) []embeddingWeight {
	return values
}

var embeddingLexicon = map[string][]embeddingWeight{
	"cache":      weights(embeddingWeight{index: 0, weight: 1}, embeddingWeight{index: 7, weight: 0.2}),
	"caches":     weights(embeddingWeight{index: 0, weight: 1}, embeddingWeight{index: 7, weight: 0.2}),
	"redis":      weights(embeddingWeight{index: 0, weight: 0.9}, embeddingWeight{index: 3, weight: 1}),
	"reset":      weights(embeddingWeight{index: 1, weight: 1.2}),
	"refresh":    weights(embeddingWeight{index: 1, weight: 0.8}),
	"preserve":   weights(embeddingWeight{index: 1, weight: 1}),
	"stale":      weights(embeddingWeight{index: 1, weight: 1}),
	"deployment": weights(embeddingWeight{index: 1, weight: 1}),
	"safe":       weights(embeddingWeight{index: 1, weight: 0.6}),
	"safely":     weights(embeddingWeight{index: 1, weight: 0.8}),
	"clear":      weights(embeddingWeight{index: 2, weight: 1.2}),
	"flush":      weights(embeddingWeight{index: 2, weight: 1.2}),
	"purge":      weights(embeddingWeight{index: 2, weight: 0.8}),
	"semantic":   weights(embeddingWeight{index: 4, weight: 1.2}),
	"trace":      weights(embeddingWeight{index: 4, weight: 1}),
	"backfill":   weights(embeddingWeight{index: 4, weight: 1}),
	"embedding":  weights(embeddingWeight{index: 4, weight: 1}),
	"embeddings": weights(embeddingWeight{index: 4, weight: 1}),
	"answer":     weights(embeddingWeight{index: 5, weight: 1}),
	"answers":    weights(embeddingWeight{index: 5, weight: 1}),
	"canonical":  weights(embeddingWeight{index: 5, weight: 1.2}),
	"alias":      weights(embeddingWeight{index: 5, weight: 0.8}),
	"keep":       weights(embeddingWeight{index: 5, weight: 0.6}),
	"gpu":        weights(embeddingWeight{index: 6, weight: 1.2}),
	"training":   weights(embeddingWeight{index: 6, weight: 1.2}),
	"cluster":    weights(embeddingWeight{index: 6, weight: 0.8}),
	"clusters":   weights(embeddingWeight{index: 6, weight: 0.8}),
	"how":        weights(embeddingWeight{index: 7, weight: 0.2}),
	"do":         weights(embeddingWeight{index: 7, weight: 0.2}),
	"i":          weights(embeddingWeight{index: 7, weight: 0.2}),
	"can":        weights(embeddingWeight{index: 7, weight: 0.2}),
	"what":       weights(embeddingWeight{index: 7, weight: 0.2}),
	"why":        weights(embeddingWeight{index: 7, weight: 0.2}),
}

func embeddings(ctx *gin.Context) {
	req := openai.EmbeddingRequest{}
	if err := ctx.ShouldBindJSON(&req); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if len(req.Input) == 0 {
		req.Input = []string{""}
	}
	data := make([]openai.Embedding, 0, len(req.Input))
	promptTokens := 0
	for index, input := range req.Input {
		vector := buildEmbeddingVector(input)
		data = append(data, openai.Embedding{Object: "embedding", Embedding: vector, Index: index})
		promptTokens += estimateEmbeddingTokens(input)
	}
	ctx.JSON(http.StatusOK, openai.EmbeddingResponse{
		Object: "list",
		Data:   data,
		Model:  req.Model,
		Usage: openai.Usage{
			PromptTokens:     promptTokens,
			CompletionTokens: 0,
			TotalTokens:      promptTokens,
		},
	})
}

func buildEmbeddingVector(text string) []float32 {
	const dimension = 64
	vector := make([]float32, dimension)
	for _, token := range tokenizeEmbeddingText(text) {
		if token == "" {
			continue
		}
		weights, ok := embeddingLexicon[token]
		if ok {
			for _, weight := range weights {
				if weight.index < 0 || weight.index >= len(vector) {
					continue
				}
				vector[weight.index] += weight.weight
			}
		}
		addHashedEmbeddingFeature(vector, token, 0.35)
	}
	if len(strings.TrimSpace(text)) > 0 {
		addHashedEmbeddingFeature(vector, strings.ToLower(strings.TrimSpace(text)), 0.25)
	}
	normalizeEmbeddingVector(vector)
	return vector
}

func tokenizeEmbeddingText(text string) []string {
	normalized := strings.ToLower(strings.TrimSpace(text))
	if normalized == "" {
		return nil
	}
	tokens := make([]string, 0, len(normalized))
	var ascii strings.Builder
	var cjk []rune
	flushASCII := func() {
		if ascii.Len() == 0 {
			return
		}
		token := strings.TrimSpace(ascii.String())
		ascii.Reset()
		if token != "" {
			tokens = append(tokens, token)
		}
	}
	flushCJK := func() {
		if len(cjk) == 0 {
			return
		}
		for index, r := range cjk {
			tokens = append(tokens, string(r))
			if index > 0 {
				tokens = append(tokens, string([]rune{cjk[index-1], r}))
			}
		}
		cjk = cjk[:0]
	}
	for _, r := range normalized {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if isCJKRune(r) {
				flushASCII()
				cjk = append(cjk, r)
			} else {
				flushCJK()
				ascii.WriteRune(r)
			}
		default:
			flushASCII()
			flushCJK()
		}
	}
	flushASCII()
	flushCJK()
	return uniqueEmbeddingTokens(tokens)
}

func estimateEmbeddingTokens(text string) int {
	tokens := tokenizeEmbeddingText(text)
	if len(tokens) == 0 {
		return 1
	}
	return len(tokens)
}

func isCJKRune(r rune) bool {
	return unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul)
}

func uniqueEmbeddingTokens(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		result = append(result, trimmed)
	}
	return result
}

func addHashedEmbeddingFeature(vector []float32, token string, weight float32) {
	if len(vector) == 0 || weight == 0 {
		return
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return
	}
	sum := sha256.Sum256([]byte(token))
	primary := int(binary.LittleEndian.Uint64(sum[:8]) % uint64(len(vector)))
	secondary := int(binary.LittleEndian.Uint64(sum[8:16]) % uint64(len(vector)))
	sign := float32(1)
	if sum[16]&1 == 1 {
		sign = -1
	}
	vector[primary] += sign * weight
	if secondary != primary {
		vector[secondary] += sign * weight * 0.5
	}
}

func normalizeEmbeddingVector(vector []float32) {
	var sum float64
	for _, value := range vector {
		sum += float64(value * value)
	}
	if sum == 0 {
		return
	}
	length := float32(1 / math.Sqrt(sum))
	for index, value := range vector {
		vector[index] = value * length
	}
}

func buildChatCompletionResponse(msg string) openai.ChatCompletionResponse {
	res := openai.ChatCompletionResponse{
		ID:      uuid.New().String(),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   "gpt-3.5-turbo-0301",
		Choices: []openai.ChatCompletionChoice{
			{
				Message: openai.ChatCompletionMessage{
					Role:    openai.ChatMessageRoleAssistant,
					Content: msg,
				},
				FinishReason: "stop",
			},
		},
		Usage: openai.Usage{
			PromptTokens:     10,
			CompletionTokens: 20,
			TotalTokens:      30,
		},
	}
	return res
}

func buildChatCompletionStreamResponseList(msg string) []openai.ChatCompletionStreamResponse {
	id := uuid.New().String()
	list := make([]openai.ChatCompletionStreamResponse, 0)
	startRes := buildChatCompletionStreamResponse(id, "", "")
	list = append(list, startRes)
	for _, delta := range msg {
		list = append(list, buildChatCompletionStreamResponse(id, string(delta), ""))
	}
	endRes := buildChatCompletionStreamResponse(id, "", "stop")
	list = append(list, endRes)
	return list
}

func buildChatCompletionStreamResponse(id, delta, finishReason string) openai.ChatCompletionStreamResponse {
	res := openai.ChatCompletionStreamResponse{
		ID:      id,
		Object:  "chat.completion.chunk",
		Created: time.Now().Unix(),
		Model:   "gpt-3.5-turbo-0301",
		Choices: []openai.ChatCompletionStreamChoice{
			{
				Index: 0,
				Delta: openai.ChatCompletionStreamChoiceDelta{
					Content: delta,
					Role:    openai.ChatMessageRoleAssistant,
				},
				FinishReason: finishReason,
			},
		},
	}
	return res
}
