import type { AxiosProgressEvent, GenericAbortSignal } from 'axios'
import { get, post } from '@/utils/request'
import { useSettingStore } from '@/store'
import { normalizeChatConfigResponse } from '@/utils/modelCatalog'

export function fetchChatAPI<T = any>(
  prompt: string,
  options?: { conversationId?: string; parentMessageId?: string; provider?: string; model?: string },
  signal?: GenericAbortSignal,
) {
  return post<T>({
    url: '/chat',
    data: {
      prompt,
      options,
      provider: options?.provider,
      model: options?.model,
    },
    signal,
  })
}

export function fetchChatConfig<T = any>() {
  return get<T>({
    url: '/config',
  }).then(response => ({
    ...response,
    data: normalizeChatConfigResponse(response.data) as T,
  }))
}

export function fetchChatAPIProcess<T = any>(
  params: {
    prompt: string
    options?: { conversationId?: string; parentMessageId?: string }
    provider?: string
    model?: string
    signal?: GenericAbortSignal
    onDownloadProgress?: (progressEvent: AxiosProgressEvent) => void },
) {
  const settingStore = useSettingStore()

  return post<T>({
    url: '/chat-process',
    data: {
      prompt: params.prompt,
      options: params.options,
      provider: params.provider,
      model: params.model,
      systemMessage: settingStore.systemMessage,
    },
    signal: params.signal,
    onDownloadProgress: params.onDownloadProgress,
  })
}

export function fetchSession<T>() {
  return post<T>({
    url: '/session',
  })
}

export function fetchVerify<T>(token: string) {
  return post<T>({
    url: '/verify',
    data: { token },
  })
}

export function fetchCode<T>(phone: string) {
  return post<T>({
    url: '/v1/sms/send/code',
    data: { phone },
  })
}

export function login<T>(phone: string, code: string) {
  return post<T>({
    url: '/v1/user/login',
    data: { user_name: phone, pwd: code, type: 1 },
  })
}
