package server

import (
	chat_context "ai-chat-service/chat-server/chat-context"
	"ai-chat-service/chat-server/data"
	metrics_bus "ai-chat-service/chat-server/metrics-bus"
	vector_data "ai-chat-service/chat-server/vector-data"
	"ai-chat-service/pkg/config"
	"ai-chat-service/pkg/log"
	"ai-chat-service/proto"
	"ai-chat-service/services/tokenizer"
	"context"
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/golang/protobuf/jsonpb"
	"github.com/google/uuid"
	"github.com/sashabaranov/go-openai"
)

type chatService struct {
	proto.UnimplementedChatServer
	config         *config.Config
	log            log.ILogger
	contextStore   chat_context.SessionStore
	data           data.IChatRecordsData        //聊天记录数据库
	vectorData     vector_data.IChatRecordsData //向量数据库
	busMetrics     *metrics_bus.BusMetrics
	fastPathOnce   sync.Once
	fastPathCache  *sessionFastPathCache
}

func NewChatService(data data.IChatRecordsData, vectorData vector_data.IChatRecordsData, config *config.Config, log log.ILogger, busMetrics *metrics_bus.BusMetrics, contextStore chat_context.SessionStore) proto.ChatServer {
	svc := &chatService{
		config:       config,
		log:          log,
		contextStore: contextStore,
		data:         data,
		vectorData:   vectorData,
		busMetrics:   busMetrics,
	}
	svc.ensureFastPathCache()
	return svc
}

func (s *chatService) ensureFastPathCache() *sessionFastPathCache {
	if s == nil {
		return nil
	}
	s.fastPathOnce.Do(func() {
		if s.fastPathCache == nil {
			s.fastPathCache = newSessionFastPathCache()
		}
	})
	return s.fastPathCache
}

func (s *chatService) ChatCompletion(ctx context.Context, in *proto.ChatCompletionRequest) (*proto.ChatCompletionResponse, error) {
	app := s.newApp(in, s.contextStore)
	if err := app.resolveSession(ctx, in); err != nil {
		s.log.Error(err)
	}
	//敏感词过滤
	ok, msg, err := app.sensitive(in)
	if err != nil {
		s.log.Error(err)
		return nil, err
	}
	if !ok {
		res := app.buildChatCompletionResponse(msg)
		return res, nil
	}

	//关键词提取
	//应用向量数据库时 考虑使用msg直接对比 而不是使用关键词对比 因为有些问题可能没有明显的关键词 但是向量数据库可以通过语义理解找到相似的问题
	keywords := app.keywords(in)
	if len(keywords) > 0 {
		idStr, score, err := s.vectorData.QueryData(context.Background(), map[string][]string{"keywords": {strings.Join(keywords, ",")}})
		if err != nil {
			s.log.Error(err)
		} else if score > 0.99 { //余弦相似度大于0.99认为是相似问题
			id, err := strconv.ParseInt(idStr, 10, 64)
			if err != nil {
				s.log.Error(err)
			} else {
				record, err := s.data.GetById(id)
				if err != nil {
					s.log.Error(err)
				} else {
					res := app.buildChatCompletionResponse(record.AIMsg)
					return res, nil
				}
			}
		}
	}
	//调用openai接口
	client := app.getOpenaiClient()

	//构建请求参数
	req, tokens, currTokens, _, err := app.buildChatCompletionRequest(ctx, in, false)
	resp, err := client.CreateChatCompletion(ctx, req)
	if err != nil {
		s.log.Error(err)
		return nil, err
	}
	//构建响应参数
	res := &proto.ChatCompletionResponse{}
	bytes, err := json.Marshal(resp)
	if err != nil {
		s.log.Error(err)
		return nil, err
	}
	//将openai接口返回的响应参数转换为proto定义的响应参数
	err = jsonpb.UnmarshalString(string(bytes), res)
	if err != nil {
		s.log.Error(err)
		return nil, err
	}
	//启用协程保存上下文和聊天记录，避免阻塞主流程
	go func() {
		err := app.saveContext(resp.ID, resp.Choices[0].Message, resp.Usage.CompletionTokens)
		if err != nil {
			s.log.Error(err)
			return
		}
	}()
	//持久化
	go func() {
		records := &data.ChatRecord{
			UserMsg:         in.Message,
			UserMsgTokens:   currTokens,
			UserMsgKeywords: keywords,
			AIMsg:           resp.Choices[0].Message.Content,
			AIMsgTokens:     resp.Usage.CompletionTokens,
			ReqTokens:       tokens,
			CreateAt:        time.Now().Unix(),
		}
		err := s.data.Add(records)
		if err != nil {
			s.log.Error(err)
			return
		}
		//保存到向量数据库
		if len(keywords) > 0 {
			list := []*vector_data.ChatRecord{
				{
					ID: strconv.FormatInt(records.ID, 10),
					KVs: map[string]string{
						"keywords": strings.Join(keywords, ","),
					},
				},
			}
			err = s.vectorData.UpsertData(context.Background(), list)
			if err != nil {
				s.log.Error(err)
				return
			}
		}
	}()
	return res, err
}
func (s *chatService) ChatCompletionStream(in *proto.ChatCompletionRequest, stream proto.Chat_ChatCompletionStreamServer) error {
	app := s.newApp(in, s.contextStore)
	if err := app.resolveSession(stream.Context(), in); err != nil {
		s.log.Error(err)
	}
	//敏感词过滤
	ok, msg, err := app.sensitive(in)
	if err != nil {
		s.busMetrics.ErrQuestionsTotalCounter.Inc()
		s.log.Error(err)
		return err
	}
	if !ok {
		s.busMetrics.SensitiveQuestionsTotalCounter.Inc()
		resId := uuid.New().String()
		startRes := app.buildChatCompletionStreamResponse(resId, "", "")
		endRes := app.buildChatCompletionStreamResponse(resId, "", "stop")
		err = stream.Send(startRes)
		if err != nil {
			s.log.Error(err)
			return err
		}
		resList := app.buildChatCompletionStreamResponseList(resId, msg)
		for _, res := range resList {
			err = stream.Send(res)
			if err != nil {
				s.log.Error(err)
				return err
			}
		}
		err = stream.Send(endRes)
		if err != nil {
			s.log.Error(err)
			return err
		}
		return nil
	}

	//关键词提取
	keywords := app.keywords(in)

	if len(keywords) > 0 {
		s.busMetrics.KeywordsQuestionsTotalCounter.Inc()
		idStr, score, err := s.vectorData.QueryData(context.Background(), map[string][]string{"keywords": {strings.Join(keywords, ",")}})
		if err != nil {
			s.log.Error(err)
		} else if score > 0.99 {
			id, err := strconv.ParseInt(idStr, 10, 64)
			if err != nil {
				s.log.Error(err)
			} else {
				record, err := s.data.GetById(id)
				if err != nil {
					s.log.Error(err)
				} else {
					resId := uuid.New().String()
					startRes := app.buildChatCompletionStreamResponse(resId, "", "")
					endRes := app.buildChatCompletionStreamResponse(resId, "", "stop")
					err = stream.Send(startRes)
					if err != nil {
						s.log.Error(err)
						return err
					}
					resList := app.buildChatCompletionStreamResponseList(resId, record.AIMsg)
					for _, res := range resList {
						err = stream.Send(res)
						if err != nil {
							s.log.Error(err)
							return err
						}
					}
					err = stream.Send(endRes)
					if err != nil {
						s.log.Error(err)
						return err
					}
					return nil
				}
			}
		}
	}

	client := app.getOpenaiClient()
	req, tokens, currTokens, _, err := app.buildChatCompletionRequest(stream.Context(), in, false)
	chatStream, err := client.CreateChatCompletionStream(stream.Context(), req)
	if err != nil {
		s.busMetrics.ErrQuestionsTotalCounter.Inc()
		s.log.Error(err)
		return err
	}
	defer chatStream.Close()
	completionContent := ""
	resultID := ""
	for {
		resp, err := chatStream.Recv()
		if err != nil && err != io.EOF {
			s.log.Error(err)
			return err
		}
		if err == io.EOF {
			break
		}
		if resultID == "" {
			resultID = resp.ID
		}
		completionContent += resp.Choices[0].Delta.Content
		res := &proto.ChatCompletionStreamResponse{}
		bytes, err := json.Marshal(resp)
		if err != nil {
			s.log.Error(err)
			return err
		}
		err = jsonpb.UnmarshalString(string(bytes), res)
		if err != nil {
			s.log.Error(err)
			return err
		}
		err = stream.Send(res)
		if err != nil {
			s.log.Error(err)
			return err
		}
	}
	resultMessage := openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleAssistant,
		Content: completionContent,
	}
	model := s.config.Chat.Model
	if in.ChatParam != nil && in.ChatParam.Model != "" {
		model = in.ChatParam.Model
	}
	resultTokens, err := tokenizer.GetTokens(&resultMessage, model)
	if err != nil {
		s.busMetrics.ErrQuestionsTotalCounter.Inc()
		s.log.Error(err)
		return err
	}

	go func() {
		err := app.saveContext(resultID, resultMessage, resultTokens)
		if err != nil {
			s.log.Error(err)
			return
		}
	}()
	go func() {
		s.busMetrics.QuestionsTotalCounter.Inc()
		records := &data.ChatRecord{
			UserMsg:         in.Message,
			UserMsgTokens:   currTokens,
			UserMsgKeywords: keywords,
			AIMsg:           completionContent,
			AIMsgTokens:     resultTokens,
			ReqTokens:       tokens,
			CreateAt:        time.Now().Unix(),
		}
		err := s.data.Add(records)
		if err != nil {
			s.log.Error(err)
			return
		}
		//保存到向量数据库
		if len(keywords) > 0 {
			list := []*vector_data.ChatRecord{
				{
					ID: strconv.FormatInt(records.ID, 10),
					KVs: map[string]string{
						"keywords": strings.Join(keywords, ","),
					},
				},
			}
			err = s.vectorData.UpsertData(context.Background(), list)
			if err != nil {
				s.log.Error(err)
				return
			}
		}
	}()
	return nil
}
