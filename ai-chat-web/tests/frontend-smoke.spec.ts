import http from 'node:http'
import type { AddressInfo } from 'node:net'
import { createApp, h, nextTick } from 'vue'
import { createPinia, setActivePinia } from 'pinia'
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import Message from '@/views/chat/components/Message/index.vue'
import { getLocalState } from '@/store/modules/chat/helper'
import { defaultSetting as defaultSettingsState, getLocalState as getSettingsLocalState } from '@/store/modules/settings/helper'
import { useChatStore } from '@/store/modules/chat'
import { extractLatestStreamChunk } from '@/utils/chatStream'
import { normalizeConversationResponse, resolveCatalogSelection } from '@/utils/modelCatalog'

type SmokeServerContext = {
  baseUrl: string
  requests: string[]
  close: () => Promise<void>
}

const mockCatalogResponse = {
  status: 'Success',
  message: null,
  data: {
    apiModel: 'openai-compatible',
    selected_provider: 'openai',
    selected_model: 'gpt-4o-mini',
    default_provider: 'openai',
    default_model: 'gpt-4o-mini',
    providers: [
      {
        provider: 'openai',
        display_name: 'OpenAI',
        enabled: true,
        is_default: true,
        supports_stream: true,
        supports_chat: true,
        supports_embedding: false,
        default_model: 'gpt-4o-mini',
        models: [
          {
            provider: 'openai',
            model: 'gpt-4o-mini',
            display_name: 'GPT-4o mini',
            enabled: true,
            is_default: true,
            supports_stream: true,
            supports_chat: true,
            supports_embedding: false,
            context_window: 128000,
          },
        ],
      },
      {
        provider: 'deepseek',
        display_name: 'DeepSeek',
        enabled: true,
        is_default: false,
        supports_stream: true,
        supports_chat: true,
        supports_embedding: false,
        default_model: 'deepseek-chat',
        models: [
          {
            provider: 'deepseek',
            model: 'deepseek-chat',
            display_name: 'DeepSeek Chat',
            enabled: true,
            is_default: true,
            supports_stream: true,
            supports_chat: true,
            supports_embedding: false,
            context_window: 64000,
          },
        ],
      },
    ],
    timeout_ms: 60000,
    reverse_proxy: 'http://127.0.0.1:8080',
  },
}

function installDomShims() {
  Object.defineProperty(window, 'matchMedia', {
    configurable: true,
    writable: true,
    value: (query: string) => ({
      matches: false,
      media: query,
      onchange: null,
      addEventListener() {},
      removeEventListener() {},
      addListener() {},
      removeListener() {},
      dispatchEvent() {
        return false
      },
    }),
  })

  Object.defineProperty(window, 'requestAnimationFrame', {
    configurable: true,
    writable: true,
    value: (callback: FrameRequestCallback) => window.setTimeout(() => callback(Date.now()), 0),
  })

  Object.defineProperty(window, 'cancelAnimationFrame', {
    configurable: true,
    writable: true,
    value: (handle: number) => window.clearTimeout(handle),
  })

  Object.defineProperty(window, 'ResizeObserver', {
    configurable: true,
    writable: true,
    value: class ResizeObserver {
      observe() {}
      unobserve() {}
      disconnect() {}
    },
  })

  Object.defineProperty(window, 'IntersectionObserver', {
    configurable: true,
    writable: true,
    value: class IntersectionObserver {
      observe() {}
      unobserve() {}
      disconnect() {}
      takeRecords() {
        return []
      }
    },
  })

  Object.defineProperty(Element.prototype, 'scrollIntoView', {
    configurable: true,
    writable: true,
    value() {},
  })
}

async function startSmokeServer(): Promise<SmokeServerContext> {
  const requests: string[] = []

  const server = http.createServer((req, res) => {
    const url = new URL(req.url ?? '/', 'http://127.0.0.1')
    const method = req.method?.toUpperCase() ?? 'GET'

    requests.push(`${method} ${url.pathname}`)

    res.setHeader('Access-Control-Allow-Origin', '*')
    res.setHeader('Access-Control-Allow-Headers', 'content-type, authorization')
    res.setHeader('Access-Control-Allow-Methods', 'GET, POST, OPTIONS')

    if (method === 'OPTIONS') {
      res.statusCode = 204
      res.end()
      return
    }

    if (url.pathname === '/config' || url.pathname === '/models') {
      res.statusCode = 200
      res.setHeader('Content-Type', 'application/json; charset=utf-8')
      res.end(JSON.stringify(mockCatalogResponse))
      return
    }

    res.statusCode = 404
    res.setHeader('Content-Type', 'application/json; charset=utf-8')
    res.end(JSON.stringify({ status: 'Error', message: 'not found', data: null }))
  })

  await new Promise<void>((resolve) => {
    server.listen(0, '127.0.0.1', () => resolve())
  })

  const address = server.address() as AddressInfo
  return {
    baseUrl: `http://127.0.0.1:${address.port}`,
    requests,
    close: () => new Promise<void>((resolve, reject) => {
      server.close((error) => {
        if (error)
          reject(error)
        else
          resolve()
      })
    }),
  }
}

async function mountMessage(trace: Chat.AnswerTrace) {
  const container = document.createElement('div')
  document.body.appendChild(container)

  const app = createApp({
    render: () => h(Message, {
      dateTime: '2026-04-07 12:00:00',
      text: 'Smoke reply',
      inversion: false,
      error: false,
      loading: false,
      trace,
      usage: {
        promptTokens: 8,
        completionTokens: 24,
        totalTokens: 32,
        usageSource: 'provider',
      },
      budget: {
        maxContextTokens: 128000,
        reservedCompletionTokens: 256,
        estimatedRemainingContextTokens: 127744,
        budgetSource: 'exact',
      },
    }),
  })

  app.mount(container)
  await nextTick()

  return {
    app,
    container,
  }
}

beforeAll(() => {
  installDomShims()
})

beforeEach(() => {
  window.localStorage.clear()
  setActivePinia(createPinia())
})

afterEach(() => {
  vi.unstubAllEnvs()
  vi.resetModules()
  document.body.innerHTML = ''
})

describe('frontend smoke', () => {
  it('loads catalog from /config and /models', async () => {
    const smokeServer = await startSmokeServer()
    try {
      vi.stubEnv('VITE_GLOB_API_URL', smokeServer.baseUrl)
      vi.resetModules()

      const { fetchChatConfig } = await import('@/api')

      const configResponse = await fetchChatConfig<Chat.ChatConfigResponse>()
      expect(smokeServer.requests).toContain('GET /config')
      expect(configResponse.data.providers).toHaveLength(2)
      expect(configResponse.data.providers[0].provider).toBe('openai')
      expect(configResponse.data.providers[0].defaultModel).toBe('gpt-4o-mini')

      const selection = resolveCatalogSelection(configResponse.data, 'deepseek', 'deepseek-chat')
      expect(selection).toEqual({ provider: 'deepseek', model: 'deepseek-chat' })

      const modelsResponse = await fetch(`${smokeServer.baseUrl}/models`)
      expect(modelsResponse.ok).toBe(true)
      const modelsPayload = await modelsResponse.json()
      expect(modelsPayload.data.providers[1].provider).toBe('deepseek')
    }
    finally {
      await smokeServer.close()
    }
  })

  it('persists provider/model selection across refresh', () => {
    const store = useChatStore()

    store.setModelSelection('deepseek', 'deepseek-chat')

    expect(getLocalState().activeProvider).toBe('deepseek')
    expect(getLocalState().activeModel).toBe('deepseek-chat')

    setActivePinia(createPinia())
    const reloadedStore = useChatStore()
    expect(reloadedStore.activeProvider).toBe('deepseek')
    expect(reloadedStore.activeModel).toBe('deepseek-chat')
  })

  it('uses a neutral default system prompt and migrates the legacy ChatGPT prompt', () => {
    window.localStorage.setItem('settingsStorage', JSON.stringify({
      systemMessage: 'You are ChatGPT, a large language model trained by OpenAI. Answer as concisely as possible.\nKnowledge cutoff: 2021-09-01\nCurrent date: 2026-04-08',
    }))

    const state = getSettingsLocalState()
    expect(defaultSettingsState().systemMessage).not.toContain('ChatGPT, a large language model trained by OpenAI')
    expect(state.systemMessage).not.toContain('ChatGPT, a large language model trained by OpenAI')
    expect(state.systemMessage).toContain('当前应用中的 AI 助手')
  })

  it('renders fallback trace details in the message card', async () => {
    const { app, container } = await mountMessage({
      answerMode: 'api',
      provider: 'deepseek',
      model: 'deepseek-chat',
      selectedProvider: 'openai',
      selectedModel: 'gpt-4o-mini',
      actualProvider: 'deepseek',
      actualModel: 'deepseek-chat',
      fallbackUsed: true,
      fallbackReason: 'invalid_model_for_provider',
      usedLocalRetrieval: true,
      cacheHit: false,
      finalAnswerSource: 'llm',
      generationProvider: 'deepseek',
      generationModel: 'deepseek-chat',
      retrievedHits: [
        {
          recordId: '88',
          score: 0.9931,
          hitType: 'exact_norm_hit',
          questionPreview: 'cached question',
          answerPreview: 'cached answer',
          provider: 'deepseek',
          model: 'deepseek-chat',
        },
      ],
    })

    try {
      const text = container.textContent ?? ''
      expect(text).toContain('知澜助手')
      expect(text).toContain('上下文使用')
      expect(text).toContain('32 已用')
      expect(text).toContain('12.8万 剩余')
      expect(text).toContain('选定 · openai / gpt-4o-mini')
      expect(text).toContain('实际 · deepseek / deepseek-chat')
      expect(text).toContain('缓存 · 未命中')
      expect(text).toContain('模式 · api')
      expect(text).toContain('回退原因：invalid_model_for_provider')
      expect(text).toContain('检索命中 · 1')
    }
    finally {
      app.unmount()
    }
  })

  it('parses the latest streamed chunk without breaking final text', () => {
    const responseText = [
      '{"id":"msg-1","sessionId":"sess-1","text":"Hel","role":"assistant","detail":{"choices":[]}}',
      '{"id":"msg-1","sessionId":"sess-1","text":"Hello","role":"assistant","trace":{"answer_mode":"api","provider":"openai","model":"gpt-4o-mini","selected_provider":"openai","selected_model":"gpt-4o-mini","actual_provider":"openai","actual_model":"gpt-4o-mini","used_local_retrieval":false,"cache_hit":false,"final_answer_source":"llm"},"detail":{"choices":[]}}',
    ].join('\n')

    const chunk = extractLatestStreamChunk(responseText)
    const parsed = normalizeConversationResponse(JSON.parse(chunk))

    expect(parsed.text).toBe('Hello')
    expect(parsed.trace?.answerMode).toBe('api')
    expect(parsed.trace?.selectedProvider).toBe('openai')
    expect(parsed.trace?.actualModel).toBe('gpt-4o-mini')
  })
})