<script setup lang='ts'>
import type { Ref } from 'vue'
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useRoute } from 'vue-router'
import { storeToRefs } from 'pinia'
import { NAutoComplete, NButton, NInput, NSelect, useDialog, useMessage } from 'naive-ui'
import html2canvas from 'html2canvas'
import { Message } from './components'
import { useScroll } from './hooks/useScroll'
import { useChat } from './hooks/useChat'
import { useCopyCode } from './hooks/useCopyCode'
import { useUsingContext } from './hooks/useUsingContext'
import HeaderComponent from './components/Header/index.vue'
import { HoverButton, SvgIcon } from '@/components/common'
import assistantAvatar from '@/assets/assistant-avatar.svg'
import { APP_NAME, APP_TAGLINE } from '@/constants/branding'
import { useBasicLayout } from '@/hooks/useBasicLayout'
import { useChatStore, usePromptStore } from '@/store'
import { fetchChatAPIProcess, fetchChatConfig } from '@/api'
import { t } from '@/locales'
import { extractLatestStreamChunk } from '@/utils/chatStream'
import { formatModelOptionLabel, formatProviderOptionLabel, normalizeConversationResponse, resolveCatalogSelection } from '@/utils/modelCatalog'

let controller = new AbortController()

const openLongReply = import.meta.env.VITE_GLOB_OPEN_LONG_REPLY === 'true'

const route = useRoute()
const dialog = useDialog()
const ms = useMessage()

const chatStore = useChatStore()

useCopyCode()

const { isMobile } = useBasicLayout()
const { addChat, updateChat, updateChatSome, getChatByUuidAndIndex } = useChat()
const { scrollRef, scrollToBottom, scrollToBottomIfAtBottom } = useScroll()
const { usingContext, toggleUsingContext } = useUsingContext()

const { uuid } = route.params as { uuid: string }

const dataSources = computed(() => chatStore.getChatByUuid(+uuid))
const currentChatHistory = computed(() => chatStore.getChatHistoryByCurrentActive)
const conversationList = computed(() => dataSources.value.filter(item => (!item.inversion && !item.error)))

const prompt = ref<string>('')
const loading = ref<boolean>(false)
const inputRef = ref<Ref | null>(null)
const modelCatalog = ref<Chat.ChatConfigResponse | null>(null)
const catalogLoading = ref(false)
const catalogError = ref('')

// 添加PromptStore
const promptStore = usePromptStore()

// 使用storeToRefs，保证store修改后，联想部分能够重新渲染
const { promptList: promptTemplate } = storeToRefs(promptStore)

const providerCatalog = computed(() => modelCatalog.value?.providers ?? [])
const currentProviderEntry = computed(() => providerCatalog.value.find(provider => provider.provider === chatStore.activeProvider) ?? null)
const currentModelEntries = computed(() => currentProviderEntry.value?.models ?? [])

const providerOptions = computed(() => providerCatalog.value.map(provider => ({
  label: formatProviderOptionLabel(provider),
  value: provider.provider,
  disabled: !provider.enabled,
})))

const modelOptions = computed(() => currentModelEntries.value.map(model => ({
  label: formatModelOptionLabel(model),
  value: model.model,
  disabled: !model.enabled,
})))

function syncSelectionFromCatalog() {
  const resolved = resolveCatalogSelection(modelCatalog.value, chatStore.activeProvider, chatStore.activeModel)
  if (resolved && (resolved.provider !== chatStore.activeProvider || resolved.model !== chatStore.activeModel))
    chatStore.setModelSelection(resolved.provider, resolved.model)
}

function handleProviderChange(provider: string) {
  const resolved = resolveCatalogSelection(modelCatalog.value, provider, chatStore.activeModel)
  if (resolved)
    chatStore.setModelSelection(resolved.provider, resolved.model)
}

function handleModelChange(model: string) {
  const resolved = resolveCatalogSelection(modelCatalog.value, chatStore.activeProvider, model)
  if (resolved)
    chatStore.setModelSelection(resolved.provider, resolved.model)
}

async function loadModelCatalog() {
  try {
    catalogLoading.value = true
    catalogError.value = ''
    modelCatalog.value = null
    const { data } = await fetchChatConfig<Chat.ChatConfigResponse>()
    modelCatalog.value = data
    syncSelectionFromCatalog()
  }
  catch (error: any) {
    catalogError.value = error?.message ?? '模型列表加载失败'
  }
  finally {
    catalogLoading.value = false
  }
}

function buildRequestOptions(
  message: string,
  options: Chat.ConversationRequest | null,
  provider = chatStore.activeProvider,
  model = chatStore.activeModel,
) {
  return {
    prompt: message,
    options: options ? { ...options } : null,
    provider,
    model,
  }
}

// 未知原因刷新页面，loading 状态不会重置，手动重置
dataSources.value.forEach((item, index) => {
  if (item.loading)
    updateChatSome(+uuid, index, { loading: false })
})

function handleSubmit() {
  onConversation()
}

async function onConversation() {
  let message = prompt.value

  if (loading.value)
    return

  if (!message || message.trim() === '')
    return

  controller = new AbortController()
  const requestProvider = chatStore.activeProvider
  const requestModel = chatStore.activeModel

  addChat(
    +uuid,
    {
      dateTime: new Date().toLocaleString(),
      text: message,
      inversion: true,
      error: false,
      conversationOptions: null,
      requestOptions: buildRequestOptions(message, null, requestProvider, requestModel),
    },
  )
  scrollToBottom()

  loading.value = true
  prompt.value = ''

  let options: Chat.ConversationRequest = {}
  const lastContext = conversationList.value[conversationList.value.length - 1]?.conversationOptions

  if (lastContext && usingContext.value)
    options = { ...lastContext }

  addChat(
    +uuid,
    {
      dateTime: new Date().toLocaleString(),
      text: '',
      loading: true,
      inversion: false,
      error: false,
      conversationOptions: null,
      requestOptions: buildRequestOptions(message, { ...options }, requestProvider, requestModel),
    },
  )
  scrollToBottom()

  try {
    let lastText = ''
    const fetchChatAPIOnce = async () => {
      await fetchChatAPIProcess<Chat.ConversationResponse>({
        prompt: message,
        options,
        provider: requestProvider,
        model: requestModel,
        signal: controller.signal,
        onDownloadProgress: ({ event }) => {
          const xhr = event.target
          const { responseText } = xhr
          const chunk = extractLatestStreamChunk(responseText)
          try {
            const data = normalizeConversationResponse(JSON.parse(chunk))
            updateChat(
              +uuid,
              dataSources.value.length - 1,
              {
                dateTime: new Date().toLocaleString(),
                text: `${lastText}${data.text ?? ''}`,
                inversion: false,
                error: false,
                loading: false,
                conversationOptions: { conversationId: data.conversationId || data.sessionId || data.id, parentMessageId: data.id },
                trace: data.trace,
                usage: data.usage,
                budget: data.budget,
                requestOptions: buildRequestOptions(message, { ...options }, requestProvider, requestModel),
              },
            )

            if (openLongReply && data.detail?.choices?.[0]?.finish_reason === 'length') {
              options.parentMessageId = data.id
              lastText = data.text
              message = ''
              return fetchChatAPIOnce()
            }

            scrollToBottomIfAtBottom()
          }
          catch (error) {
          //
          }
        },
      })
    }

    await fetchChatAPIOnce()
  }
  catch (error: any) {
    const errorMessage = error?.message ?? t('common.wrong')

    if (error.message === 'canceled') {
      updateChatSome(
        +uuid,
        dataSources.value.length - 1,
        {
          loading: false,
        },
      )
      scrollToBottomIfAtBottom()
      return
    }

    const currentChat = getChatByUuidAndIndex(+uuid, dataSources.value.length - 1)

    if (currentChat?.text && currentChat.text !== '') {
      updateChatSome(
        +uuid,
        dataSources.value.length - 1,
        {
          text: `${currentChat.text}\n[${errorMessage}]`,
          error: false,
          loading: false,
        },
      )
      return
    }

    updateChat(
      +uuid,
      dataSources.value.length - 1,
      {
        dateTime: new Date().toLocaleString(),
        text: errorMessage,
        inversion: false,
        error: true,
        loading: false,
        conversationOptions: null,
        requestOptions: buildRequestOptions(message, { ...options }, requestProvider, requestModel),
      },
    )
    scrollToBottomIfAtBottom()
  }
  finally {
    loading.value = false
  }
}

async function onRegenerate(index: number) {
  if (loading.value)
    return

  controller = new AbortController()

  const { requestOptions } = dataSources.value[index]

  let message = requestOptions?.prompt ?? ''

  let options: Chat.ConversationRequest = {}

  if (requestOptions.options)
    options = { ...requestOptions.options }

  const requestProvider = requestOptions.provider || chatStore.activeProvider
  const requestModel = requestOptions.model || chatStore.activeModel

  loading.value = true

  updateChat(
    +uuid,
    index,
    {
      dateTime: new Date().toLocaleString(),
      text: '',
      inversion: false,
      error: false,
      loading: true,
      conversationOptions: null,
      requestOptions: {
        prompt: message,
        options: { ...options },
        provider: requestProvider,
        model: requestModel,
      },
    },
  )

  try {
    let lastText = ''
    const fetchChatAPIOnce = async () => {
      await fetchChatAPIProcess<Chat.ConversationResponse>({
        prompt: message,
        options,
        provider: requestProvider,
        model: requestModel,
        signal: controller.signal,
        onDownloadProgress: ({ event }) => {
          const xhr = event.target
          const { responseText } = xhr
          const chunk = extractLatestStreamChunk(responseText)
          try {
            const data = normalizeConversationResponse(JSON.parse(chunk))
            updateChat(
              +uuid,
              index,
              {
                dateTime: new Date().toLocaleString(),
                text: `${lastText}${data.text ?? ''}`,
                inversion: false,
                error: false,
                loading: false,
                conversationOptions: { conversationId: data.conversationId || data.sessionId || data.id, parentMessageId: data.id },
                trace: data.trace,
                usage: data.usage,
                budget: data.budget,
                requestOptions: {
                  prompt: message,
                  options: { ...options },
                  provider: requestProvider,
                  model: requestModel,
                },
              },
            )

            if (openLongReply && data.detail?.choices?.[0]?.finish_reason === 'length') {
              options.parentMessageId = data.id
              lastText = data.text
              message = ''
              return fetchChatAPIOnce()
            }
          }
          catch (error) {
            //
          }
        },
      })
    }
    await fetchChatAPIOnce()
  }
  catch (error: any) {
    if (error.message === 'canceled') {
      updateChatSome(
        +uuid,
        index,
        {
          loading: false,
        },
      )
      return
    }

    const errorMessage = error?.message ?? t('common.wrong')

    updateChat(
      +uuid,
      index,
      {
        dateTime: new Date().toLocaleString(),
        text: errorMessage,
        inversion: false,
        error: true,
        loading: false,
        conversationOptions: null,
        requestOptions: {
          prompt: message,
          options: { ...options },
          provider: requestProvider,
          model: requestModel,
        },
      },
    )
  }
  finally {
    loading.value = false
  }
}

function handleExport() {
  if (loading.value)
    return

  const d = dialog.warning({
    title: t('chat.exportImage'),
    content: t('chat.exportImageConfirm'),
    positiveText: t('common.yes'),
    negativeText: t('common.no'),
    onPositiveClick: async () => {
      try {
        d.loading = true
        const ele = document.getElementById('image-wrapper')
        const canvas = await html2canvas(ele as HTMLDivElement, {
          useCORS: true,
        })
        const imgUrl = canvas.toDataURL('image/png')
        const tempLink = document.createElement('a')
        tempLink.style.display = 'none'
        tempLink.href = imgUrl
        tempLink.setAttribute('download', 'chat-shot.png')
        if (typeof tempLink.download === 'undefined')
          tempLink.setAttribute('target', '_blank')

        document.body.appendChild(tempLink)
        tempLink.click()
        document.body.removeChild(tempLink)
        window.URL.revokeObjectURL(imgUrl)
        d.loading = false
        ms.success(t('chat.exportSuccess'))
        Promise.resolve()
      }
      catch (error: any) {
        ms.error(t('chat.exportFailed'))
      }
      finally {
        d.loading = false
      }
    },
  })
}

function handleDelete(index: number) {
  if (loading.value)
    return

  dialog.warning({
    title: t('chat.deleteMessage'),
    content: t('chat.deleteMessageConfirm'),
    positiveText: t('common.yes'),
    negativeText: t('common.no'),
    onPositiveClick: () => {
      chatStore.deleteChatByUuid(+uuid, index)
    },
  })
}

function handleClear() {
  if (loading.value)
    return

  dialog.warning({
    title: t('chat.clearChat'),
    content: t('chat.clearChatConfirm'),
    positiveText: t('common.yes'),
    negativeText: t('common.no'),
    onPositiveClick: () => {
      chatStore.clearChatByUuid(+uuid)
    },
  })
}

function handleEnter(event: KeyboardEvent) {
  if (!isMobile.value) {
    if (event.key === 'Enter' && !event.shiftKey) {
      event.preventDefault()
      handleSubmit()
    }
  }
  else {
    if (event.key === 'Enter' && event.ctrlKey) {
      event.preventDefault()
      handleSubmit()
    }
  }
}

function handleStop() {
  if (loading.value) {
    controller.abort()
    loading.value = false
  }
}

// 可优化部分
// 搜索选项计算，这里使用value作为索引项，所以当出现重复value时渲染异常(多项同时出现选中效果)
// 理想状态下其实应该是key作为索引项,但官方的renderOption会出现问题，所以就需要value反renderLabel实现
const searchOptions = computed(() => {
  if (prompt.value.startsWith('/')) {
    return promptTemplate.value.filter((item: { key: string }) => item.key.toLowerCase().includes(prompt.value.substring(1).toLowerCase())).map((obj: { value: any }) => {
      return {
        label: obj.value,
        value: obj.value,
      }
    })
  }
  else {
    return []
  }
})

// value反渲染key
const renderOption = (option: { label: string }) => {
  for (const i of promptTemplate.value) {
    if (i.value === option.label)
      return [i.key]
  }
  return []
}

const placeholder = computed(() => {
  if (isMobile.value)
    return t('chat.placeholderMobile')
  return t('chat.placeholder')
})

const buttonDisabled = computed(() => {
  return loading.value || !prompt.value || prompt.value.trim() === ''
})

const footerClass = computed(() => {
  let classes = ['p-4']
  if (isMobile.value)
    classes = ['sticky', 'left-0', 'bottom-0', 'right-0', 'p-2', 'pr-3', 'overflow-hidden']
  return classes
})

onMounted(() => {
  void loadModelCatalog()
  scrollToBottom()
  if (inputRef.value && !isMobile.value)
    inputRef.value?.focus()
})

onUnmounted(() => {
  if (loading.value)
    controller.abort()
})
</script>

<template>
  <div class="flex h-full w-full flex-col">
    <HeaderComponent
      v-if="isMobile"
      :using-context="usingContext"
      @export="handleExport"
      @toggle-using-context="toggleUsingContext"
    />
    <main class="flex-1 overflow-hidden">
      <div
        id="scrollRef"
        ref="scrollRef"
        class="h-full overflow-hidden overflow-y-auto"
      >
        <div
          id="image-wrapper"
          class="relative m-auto w-full max-w-screen-xl overflow-hidden rounded-[32px] border border-white/70 bg-white/80 shadow-[0_24px_80px_rgba(15,23,42,0.08)] backdrop-blur-xl dark:border-white/10 dark:bg-[#0b1020]/85"
          :class="[isMobile ? 'p-2' : 'p-4']"
        >
          <div class="pointer-events-none absolute inset-0 bg-[radial-gradient(circle_at_top_left,_rgba(16,185,129,0.08),_transparent_28%),radial-gradient(circle_at_top_right,_rgba(59,130,246,0.08),_transparent_26%)] dark:bg-[radial-gradient(circle_at_top_left,_rgba(16,185,129,0.08),_transparent_28%),radial-gradient(circle_at_top_right,_rgba(59,130,246,0.08),_transparent_26%)]" />
          <div class="relative z-10">
            <div
              v-if="!isMobile"
              class="mb-4 flex items-center justify-between gap-4 rounded-[28px] border border-white/70 bg-white/75 px-4 py-3 shadow-sm backdrop-blur dark:border-white/10 dark:bg-white/5"
            >
              <div class="flex min-w-0 items-center gap-3">
                <div class="h-11 w-11 overflow-hidden rounded-2xl border border-white/70 bg-white/80 p-1 shadow-sm dark:border-white/10 dark:bg-white/5">
                  <img :src="assistantAvatar" alt="知澜助手" class="h-full w-full rounded-[14px] object-cover" />
                </div>
                <div class="min-w-0">
                  <p class="text-[11px] font-semibold uppercase tracking-[0.24em] text-slate-400">
                    {{ APP_NAME }}
                  </p>
                  <p class="truncate text-sm font-medium text-slate-700 dark:text-slate-200">
                    {{ APP_TAGLINE }}
                  </p>
                </div>
              </div>
              <div class="hidden items-center gap-2 text-[11px] text-slate-500 dark:text-slate-400 sm:flex">
                <span class="rounded-full border border-slate-200/70 bg-white/70 px-2.5 py-1 dark:border-white/10 dark:bg-white/5">
                  {{ usingContext ? '上下文开启' : '上下文关闭' }}
                </span>
                <span
                  v-if="currentChatHistory"
                  class="max-w-[16rem] truncate rounded-full border border-slate-200/70 bg-white/70 px-2.5 py-1 dark:border-white/10 dark:bg-white/5"
                >
                  {{ currentChatHistory.title }}
                </span>
              </div>
            </div>

            <template v-if="!dataSources.length">
              <div class="flex min-h-[52vh] flex-col items-center justify-center px-4 text-center">
                <div class="h-16 w-16 overflow-hidden rounded-[28px] border border-white/70 bg-white/80 p-1.5 shadow-sm dark:border-white/10 dark:bg-white/5">
                  <img :src="assistantAvatar" alt="知澜助手" class="h-full w-full rounded-[22px] object-cover" />
                </div>
                <p class="mt-5 text-lg font-semibold text-slate-900 dark:text-white">
                  {{ APP_NAME }}
                </p>
                <p class="mt-2 text-sm text-slate-500 dark:text-slate-400">
                  {{ APP_TAGLINE }}
                </p>
                <p class="mt-3 max-w-lg text-xs leading-6 text-slate-400 dark:text-slate-500">
                  模型切换、上下文续写和检索状态会以轻量辅助信息呈现，正文保持干净。
                </p>
              </div>
            </template>

            <template v-else>
              <div>
                <Message
                  v-for="(item, index) of dataSources"
                  :key="index"
                  :date-time="item.dateTime"
                  :text="item.text"
                  :inversion="item.inversion"
                  :error="item.error"
                  :loading="item.loading"
                  :trace="item.trace"
                  :usage="item.usage"
                  :budget="item.budget"
                  @regenerate="onRegenerate(index)"
                  @delete="handleDelete(index)"
                />
                <div class="sticky bottom-0 left-0 flex justify-center">
                  <NButton v-if="loading" type="warning" @click="handleStop">
                    <template #icon>
                      <SvgIcon icon="ri:stop-circle-line" />
                    </template>
                    Stop Responding
                  </NButton>
                </div>
              </div>
            </template>
          </div>
        </div>
      </div>
    </main>

    <footer :class="footerClass">
      <div class="w-full max-w-screen-xl m-auto rounded-[28px] border border-white/70 bg-white/85 p-3 shadow-[0_18px_60px_rgba(15,23,42,0.08)] backdrop-blur-xl dark:border-white/10 dark:bg-[#10151f]/85">
        <div class="space-y-3">
          <p v-if="catalogError" class="text-xs text-amber-500">
            {{ catalogError }}
          </p>
          <div class="grid gap-2 md:grid-cols-2">
            <NSelect
              :value="chatStore.activeProvider"
              :options="providerOptions"
              :loading="catalogLoading"
              :disabled="catalogLoading || !providerOptions.length"
              size="small"
              placeholder="选择 Provider"
              @update:value="handleProviderChange"
            />
            <NSelect
              :value="chatStore.activeModel"
              :options="modelOptions"
              :loading="catalogLoading"
              :disabled="catalogLoading || !modelOptions.length"
              size="small"
              placeholder="选择 Model"
              @update:value="handleModelChange"
            />
          </div>
          <div class="flex items-center justify-between space-x-2">
            <HoverButton @click="handleClear">
              <span class="text-xl text-[#4f555e] dark:text-white">
                <SvgIcon icon="ri:delete-bin-line" />
              </span>
            </HoverButton>
            <HoverButton v-if="!isMobile" @click="handleExport">
              <span class="text-xl text-[#4f555e] dark:text-white">
                <SvgIcon icon="ri:download-2-line" />
              </span>
            </HoverButton>
            <HoverButton v-if="!isMobile" @click="toggleUsingContext">
              <span class="text-xl" :class="{ 'text-[#4b9e5f]': usingContext, 'text-[#a8071a]': !usingContext }">
                <SvgIcon icon="ri:chat-history-line" />
              </span>
            </HoverButton>
            <NAutoComplete v-model:value="prompt" :options="searchOptions" :render-label="renderOption">
              <template #default="{ handleInput, handleBlur, handleFocus }">
                <NInput
                  ref="inputRef"
                  v-model:value="prompt"
                  type="textarea"
                  :placeholder="placeholder"
                  :autosize="{ minRows: 1, maxRows: isMobile ? 4 : 8 }"
                  @input="handleInput"
                  @focus="handleFocus"
                  @blur="handleBlur"
                  @keypress="handleEnter"
                />
              </template>
            </NAutoComplete>
            <NButton type="primary" :disabled="buttonDisabled" @click="handleSubmit">
              <template #icon>
                <span class="dark:text-black">
                  <SvgIcon icon="ri:send-plane-fill" />
                </span>
              </template>
            </NButton>
          </div>
        </div>
      </div>
    </footer>
  </div>
</template>
