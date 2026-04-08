function toText(value: unknown): string {
  return typeof value === 'string' ? value.trim() : ''
}

function toNumber(value: unknown): number | undefined {
  if (typeof value === 'number' && Number.isFinite(value))
    return value
  if (typeof value === 'string' && value.trim() !== '') {
    const parsed = Number(value)
    if (Number.isFinite(parsed))
      return parsed
  }
  return undefined
}

function toBoolean(value: unknown, fallback = false): boolean {
  if (typeof value === 'boolean')
    return value
  return fallback
}

function toRetrievalHit(raw: any): Chat.RetrievalHit {
  return {
    recordId: toText(raw?.recordId ?? raw?.record_id),
    score: toNumber(raw?.score) ?? 0,
    hitType: toText(raw?.hitType ?? raw?.hit_type),
    questionPreview: toText(raw?.questionPreview ?? raw?.question_preview),
    answerPreview: toText(raw?.answerPreview ?? raw?.answer_preview),
    provider: toText(raw?.provider),
    model: toText(raw?.model),
  }
}

function toModelCatalogEntry(raw: any): Chat.ModelCatalogEntry {
  const model = toText(raw?.model ?? raw?.name)
  const displayName = toText(raw?.displayName ?? raw?.display_name) || model
  return {
    provider: toText(raw?.provider),
    model,
    displayName,
    enabled: toBoolean(raw?.enabled, true),
    isDefault: toBoolean(raw?.isDefault ?? raw?.is_default),
    supportsStream: toBoolean(raw?.supportsStream ?? raw?.supports_stream, true),
    supportsChat: toBoolean(raw?.supportsChat ?? raw?.supports_chat, true),
    supportsEmbedding: toBoolean(raw?.supportsEmbedding ?? raw?.supports_embedding),
    contextWindow: toNumber(raw?.contextWindow ?? raw?.context_window),
  }
}

function toProviderCatalogEntry(raw: any): Chat.ProviderCatalogEntry {
  const models = Array.isArray(raw?.models) ? raw.models.map(toModelCatalogEntry) : []
  const provider = toText(raw?.provider ?? raw?.name)
  const displayName = toText(raw?.displayName ?? raw?.display_name) || provider
  const defaultModel = toText(raw?.defaultModel ?? raw?.default_model)
    || models.find((model: Chat.ModelCatalogEntry) => model.isDefault && model.enabled)?.model
    || models.find((model: Chat.ModelCatalogEntry) => model.enabled)?.model
    || models[0]?.model
    || ''

  return {
    provider,
    displayName,
    enabled: toBoolean(raw?.enabled, true),
    isDefault: toBoolean(raw?.isDefault ?? raw?.is_default),
    supportsStream: toBoolean(raw?.supportsStream ?? raw?.supports_stream, true),
    supportsChat: toBoolean(raw?.supportsChat ?? raw?.supports_chat, true),
    supportsEmbedding: toBoolean(raw?.supportsEmbedding ?? raw?.supports_embedding),
    contextWindow: toNumber(raw?.contextWindow ?? raw?.context_window),
    defaultModel,
    models,
  }
}

function pickFallbackModel(provider: Chat.ProviderCatalogEntry): Chat.ModelCatalogEntry | null {
  return provider.models.find(model => model.enabled && model.isDefault)
    ?? provider.models.find(model => model.enabled)
    ?? provider.models[0]
    ?? null
}

function pickFallbackProvider(catalog: Chat.ChatConfigResponse | null | undefined): Chat.ProviderCatalogEntry | null {
  const providers = catalog?.providers ?? []
  return providers.find(provider => provider.enabled && provider.isDefault)
    ?? providers.find(provider => provider.enabled)
    ?? providers[0]
    ?? null
}

export function formatProviderLabel(provider: Chat.ProviderCatalogEntry): string {
  return provider.displayName || provider.provider
}

export function formatProviderOptionLabel(provider: Chat.ProviderCatalogEntry): string {
  const labels = [formatProviderLabel(provider)]
  if (provider.isDefault)
    labels.push('默认')
  if (!provider.enabled)
    labels.push('停用')
  return labels.join(' · ')
}

export function formatModelLabel(model: Chat.ModelCatalogEntry): string {
  return model.displayName || model.model
}

export function formatModelOptionLabel(model: Chat.ModelCatalogEntry): string {
  const labels = [formatModelLabel(model)]
  if (model.isDefault)
    labels.push('默认')
  if (!model.enabled)
    labels.push('停用')
  return labels.join(' · ')
}

export function normalizeChatConfigResponse(raw: any): Chat.ChatConfigResponse {
  const providers = Array.isArray(raw?.providers)
    ? raw.providers.map(toProviderCatalogEntry)
    : []

  const selectedProvider = toText(raw?.selectedProvider)
  const selectedModel = toText(raw?.selectedModel)
  const defaultProvider = toText(raw?.defaultProvider)
  const defaultModel = toText(raw?.defaultModel)
  const providerFallback = pickFallbackProvider({ providers } as Chat.ChatConfigResponse)

  return {
    apiModel: toText(raw?.apiModel),
    selectedProvider: selectedProvider || providerFallback?.provider || defaultProvider,
    selectedModel: selectedModel || providerFallback?.defaultModel || defaultModel,
    defaultProvider: defaultProvider || providerFallback?.provider || '',
    defaultModel: defaultModel || providerFallback?.defaultModel || '',
    providers,
    timeoutMs: toNumber(raw?.timeoutMs),
    socksProxy: toText(raw?.socksProxy),
    httpsProxy: toText(raw?.httpsProxy),
    reverseProxy: toText(raw?.reverseProxy),
    balance: toText(raw?.balance),
  }
}

function normalizeUsageInfo(raw: any): Chat.UsageInfo {
  return {
    promptTokens: toNumber(raw?.promptTokens ?? raw?.prompt_tokens),
    completionTokens: toNumber(raw?.completionTokens ?? raw?.completion_tokens),
    totalTokens: toNumber(raw?.totalTokens ?? raw?.total_tokens),
    usageSource: toText(raw?.usageSource ?? raw?.usage_source) as Chat.UsageInfo['usageSource'],
  }
}

function normalizeBudgetInfo(raw: any): Chat.BudgetInfo {
  return {
    maxContextTokens: toNumber(raw?.maxContextTokens ?? raw?.max_context_tokens),
    reservedCompletionTokens: toNumber(raw?.reservedCompletionTokens ?? raw?.reserved_completion_tokens),
    estimatedRemainingContextTokens: toNumber(raw?.estimatedRemainingContextTokens ?? raw?.estimated_remaining_context_tokens),
    budgetSource: toText(raw?.budgetSource ?? raw?.budget_source) as Chat.BudgetInfo['budgetSource'],
  }
}

export function normalizeAnswerTrace(raw: any): Chat.AnswerTrace {
  const selectedProvider = toText(raw?.selectedProvider ?? raw?.selected_provider)
  const selectedModel = toText(raw?.selectedModel ?? raw?.selected_model)
  const actualProvider = toText(raw?.actualProvider ?? raw?.actual_provider)
  const actualModel = toText(raw?.actualModel ?? raw?.actual_model)
  const provider = toText(raw?.provider) || actualProvider || selectedProvider
  const model = toText(raw?.model) || actualModel || selectedModel
  const normalizedHits = Array.isArray(raw?.retrievedHits ?? raw?.retrieved_hits)
    ? (raw.retrievedHits ?? raw.retrieved_hits).map(toRetrievalHit)
    : undefined

  return {
    answerMode: toText(raw?.answerMode ?? raw?.answer_mode) as Chat.AnswerTrace['answerMode'],
    provider,
    model,
    selectedProvider: selectedProvider || provider,
    selectedModel: selectedModel || model,
    actualProvider: actualProvider || provider,
    actualModel: actualModel || model,
    fallbackUsed: toBoolean(raw?.fallbackUsed ?? raw?.fallback_used),
    fallbackReason: toText(raw?.fallbackReason ?? raw?.fallback_reason),
    usedLocalRetrieval: toBoolean(raw?.usedLocalRetrieval ?? raw?.used_local_retrieval),
    cacheHit: toBoolean(raw?.cacheHit ?? raw?.cache_hit),
    finalAnswerSource: toText(raw?.finalAnswerSource ?? raw?.final_answer_source),
    generationProvider: toText(raw?.generationProvider ?? raw?.generation_provider),
    generationModel: toText(raw?.generationModel ?? raw?.generation_model),
    retrievedHits: normalizedHits,
  }
}

export function normalizeConversationResponse(raw: any): Chat.ConversationResponse {
  return {
    conversationId: toText(raw?.conversationId),
    trace: raw?.trace ? normalizeAnswerTrace(raw.trace) : undefined,
    usage: raw?.usage ? normalizeUsageInfo(raw.usage) : undefined,
    budget: raw?.budget ? normalizeBudgetInfo(raw.budget) : undefined,
    detail: (raw?.detail ?? { choices: [] }) as Chat.ConversationResponse['detail'],
    id: toText(raw?.id),
    parentMessageId: toText(raw?.parentMessageId),
    role: toText(raw?.role),
    text: toText(raw?.text),
    sessionId: toText(raw?.sessionId),
  }
}

export function resolveCatalogSelection(
  catalog: Chat.ChatConfigResponse | null | undefined,
  preferredProvider?: string,
  preferredModel?: string,
): { provider: string; model: string } | null {
  const providers = catalog?.providers ?? []
  if (!providers.length)
    return null

  const resolveProvider = (providerName?: string) => {
    const trimmed = toText(providerName)
    if (!trimmed)
      return null
    return providers.find(provider => provider.provider === trimmed && provider.enabled) ?? null
  }

  const provider = resolveProvider(preferredProvider)
    || resolveProvider(catalog?.selectedProvider)
    || resolveProvider(catalog?.defaultProvider)
    || providers.find(provider => provider.enabled)
    || providers[0]

  if (!provider)
    return null

  const resolveModel = (modelName?: string) => {
    const trimmed = toText(modelName)
    if (!trimmed)
      return null
    return provider.models.find(model => model.model === trimmed && model.enabled) ?? null
  }

  const model = resolveModel(preferredModel)
    || resolveModel(catalog?.selectedModel)
    || resolveModel(catalog?.defaultModel)
    || pickFallbackModel(provider)

  if (!model)
    return null

  return {
    provider: provider.provider,
    model: model.model,
  }
}