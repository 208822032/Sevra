declare namespace Chat {

	interface Chat {
		dateTime: string
		text: string
		inversion?: boolean
		error?: boolean
		loading?: boolean
		conversationOptions?: ConversationRequest | null
		requestOptions: { prompt: string; options?: ConversationRequest | null; provider?: string; model?: string }
		trace?: AnswerTrace
		usage?: UsageInfo
		budget?: BudgetInfo
	}

	interface History {
		title: string
		isEdit: boolean
		uuid: number
	}

	interface ChatState {
		active: number | null
		usingContext: boolean;
		activeProvider: string
		activeModel: string
		history: History[]
		chat: { uuid: number; data: Chat[] }[]
	}

	interface ModelCatalogEntry {
		provider: string
		model: string
		displayName: string
		enabled: boolean
		isDefault: boolean
		supportsStream: boolean
		supportsChat: boolean
		supportsEmbedding: boolean
		contextWindow?: number
	}

	interface ProviderCatalogEntry {
		provider: string
		displayName: string
		enabled: boolean
		isDefault: boolean
		supportsStream: boolean
		supportsChat: boolean
		supportsEmbedding: boolean
		contextWindow?: number
		defaultModel: string
		models: ModelCatalogEntry[]
	}

	interface ChatConfigResponse {
		apiModel: string
		selectedProvider: string
		selectedModel: string
		defaultProvider: string
		defaultModel: string
		providers: ProviderCatalogEntry[]
		timeoutMs?: number
		socksProxy?: string
		httpsProxy?: string
		reverseProxy?: string
		balance?: string
	}

	interface ConversationRequest {
		conversationId?: string
		parentMessageId?: string
	}

	interface UsageInfo {
		promptTokens?: number
		completionTokens?: number
		totalTokens?: number
		usageSource?: 'provider' | 'estimated'
	}

	interface BudgetInfo {
		maxContextTokens?: number
		reservedCompletionTokens?: number
		estimatedRemainingContextTokens?: number
		budgetSource?: 'exact' | 'estimated' | 'fallback'
	}

	interface RetrievalHit {
		recordId: string
		score: number
		hitType: string
		questionPreview: string
		answerPreview: string
		provider: string
		model: string
	}

	interface AnswerTrace {
		answerMode: 'api' | 'local' | 'hybrid' | 'cache_hit'
		provider: string
		model: string
		selectedProvider?: string
		selectedModel?: string
		actualProvider?: string
		actualModel?: string
		fallbackUsed?: boolean
		fallbackReason?: string
		usedLocalRetrieval: boolean
		cacheHit: boolean
		finalAnswerSource: string
		generationProvider?: string
		generationModel?: string
		retrievedHits?: RetrievalHit[]
	}

	interface ConversationResponse {
		conversationId: string
		trace?: AnswerTrace
		usage?: UsageInfo
		budget?: BudgetInfo
		detail: {
			choices: { finish_reason: string; index: number; logprobs: any; text: string }[]
			created: number
			id: string
			model: string
			object: string
			usage: { completion_tokens: number; prompt_tokens: number; total_tokens: number }
		}
		id: string
		parentMessageId: string
		role: string
		text: string
		sessionId?: string
	}
}
