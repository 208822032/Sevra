import { ss } from '@/utils/storage'

const LOCAL_NAME = 'settingsStorage'
const LEGACY_SYSTEM_MESSAGE_PREFIX = 'You are ChatGPT, a large language model trained by OpenAI. Answer as concisely as possible.'

export interface SettingsState {
  systemMessage: string
}

export function defaultSetting(): SettingsState {
  const currentDate = new Date().toISOString().split('T')[0]
  return {
    systemMessage: `你是一个AI助手。回答时保持简洁、准确、中文优先。不要主动声称自己是 ChatGPT、OpenAI 或其他特定厂商的官方模型；如果用户询问你的身份，请说明你是当前应用中的 AI 助手，具体模型由系统配置决定。\n知识截止：2021-09-01\n当前日期：${currentDate}`,
  }
}

function isLegacySystemMessage(systemMessage?: string): boolean {
  return typeof systemMessage === 'string' && systemMessage.trim().startsWith(LEGACY_SYSTEM_MESSAGE_PREFIX)
}

export function getLocalState(): SettingsState {
  const defaults = defaultSetting()
  const localSetting: SettingsState | undefined = ss.get(LOCAL_NAME)
  if (!localSetting)
    return defaults

  if (isLegacySystemMessage(localSetting.systemMessage))
    return { ...defaults, ...localSetting, systemMessage: defaults.systemMessage }

  return { ...defaults, ...localSetting }
}

export function setLocalState(setting: SettingsState): void {
  ss.set(LOCAL_NAME, setting)
}

export function removeLocalState() {
  ss.remove(LOCAL_NAME)
}
