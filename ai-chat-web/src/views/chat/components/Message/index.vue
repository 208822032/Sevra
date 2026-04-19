<script setup lang='ts'>
import { computed, ref } from 'vue'
import { NDropdown } from 'naive-ui'
import AvatarComponent from './Avatar.vue'
import TextComponent from './Text.vue'
import { APP_NAME } from '@/constants/branding'
import { SvgIcon } from '@/components/common'
import { copyText } from '@/utils/format'
import { useIconRender } from '@/hooks/useIconRender'
import { t } from '@/locales'
import { useBasicLayout } from '@/hooks/useBasicLayout'

interface Props {
  dateTime?: string
  text?: string
  inversion?: boolean
  error?: boolean
  loading?: boolean
  trace?: Chat.AnswerTrace
  usage?: Chat.UsageInfo
  budget?: Chat.BudgetInfo
}

interface Emit {
  (ev: 'regenerate'): void
  (ev: 'delete'): void
}

const props = defineProps<Props>()

const emit = defineEmits<Emit>()

const { isMobile } = useBasicLayout()

const { iconRender } = useIconRender()

const textRef = ref<HTMLElement>()

const asRawText = ref(props.inversion)

const messageRef = ref<HTMLElement>()

const senderLabel = computed(() => (props.inversion ? '你' : APP_NAME))

const selectedLabel = computed(() => {
  if (!props.trace)
    return '-'
  const provider = props.trace.selectedProvider || props.trace.provider || ''
  const model = props.trace.selectedModel || props.trace.model || ''
  return [provider, model].filter(Boolean).join(' / ') || '-'
})

const actualLabel = computed(() => {
  if (!props.trace)
    return '-'
  const provider = props.trace.actualProvider || props.trace.provider || props.trace.selectedProvider || ''
  const model = props.trace.actualModel || props.trace.model || props.trace.selectedModel || ''
  return [provider, model].filter(Boolean).join(' / ') || '-'
})

const answerSourceKind = computed(() => {
  const kind = props.trace?.answerSourceKind?.trim().toLowerCase()
  if (kind === 'cache' || kind === 'ai')
    return kind
  if (props.trace?.cacheHit)
    return 'cache'
  if ((props.trace?.finalAnswerSource || '').startsWith('qa_') || props.trace?.finalAnswerSource === 'local_cache')
    return 'cache'
  return 'ai'
})

const answerSourceLabel = computed(() => (answerSourceKind.value === 'cache' ? '缓存回答' : 'AI 回答'))

const answerSourceDetail = computed(() => {
  if (!props.trace)
    return ''
  if (answerSourceKind.value === 'cache') {
    const detail: string[] = ['本地命中']
    if (props.trace.qaHitType)
      detail.push(props.trace.qaHitType)
    if (props.trace.semanticModeUsed)
      detail.push(`语义 ${props.trace.semanticModeUsed}`)
    return detail.join(' · ')
  }
  if (props.trace.usedLocalRetrieval || props.trace.finalAnswerSource === 'hybrid_llm')
    return 'AI 生成 · 使用本地检索上下文'
  return 'AI 生成'
})

const semanticModeLabel = computed(() => {
  if (!props.trace)
    return ''
  const requested = props.trace.semanticModeRequested?.trim() || ''
  const used = props.trace.semanticModeUsed?.trim() || ''
  if (!requested && !used)
    return ''
  if (requested && used && requested !== used)
    return `${requested} → ${used}`
  return used || requested
})

const semanticFallbackNote = computed(() => {
  if (!props.trace?.semanticFallbackUsed)
    return ''
  return props.trace.semanticFallbackReason ? `语义回退：${props.trace.semanticFallbackReason}` : '语义回退'
})

function formatTokenCount(value: number) {
  if (value >= 10000)
    return `${(value / 10000).toFixed(value % 10000 === 0 ? 0 : 1)}万`
  return new Intl.NumberFormat('zh-CN').format(value)
}

const tokenPromptCount = computed(() => props.usage?.promptTokens ?? 0)

const tokenCompletionCount = computed(() => props.usage?.completionTokens ?? 0)

const tokenTotalCount = computed(() => props.usage?.totalTokens ?? (tokenPromptCount.value + tokenCompletionCount.value))

const contextRemainingTokens = computed(() => props.budget?.estimatedRemainingContextTokens ?? 0)

const contextUsagePercent = computed(() => {
  const used = tokenTotalCount.value
  const remaining = contextRemainingTokens.value
  const total = used + remaining
  if (total <= 0)
    return 0
  return Number(((used / total) * 100).toFixed(2))
})

const tokenUsageLabel = computed(() => {
  if (!props.usage)
    return ''
  if (answerSourceKind.value === 'cache')
    return '缓存命中 / 非 AI 调用'
  if (props.usage.usageSource === 'provider')
    return '模型计数'
  if (props.usage.usageSource === 'estimated')
    return '本地估算'
  return props.usage.usageSource || ''
})

const tokenUsageNote = computed(() => {
  if (answerSourceKind.value !== 'cache')
    return ''
  return '本次未调用 AI，下面的 token 为本地估算'
})

const contextBarStyle = computed(() => ({
  width: contextUsagePercent.value > 0 ? `max(${contextUsagePercent.value}%, 8px)` : '0%',
}))

const fallbackNote = computed(() => {
  if (!props.trace)
    return ''
  if (!props.trace.fallbackUsed && selectedLabel.value === actualLabel.value)
    return ''
  return props.trace.fallbackReason ? `回退原因：${props.trace.fallbackReason}` : '已自动回退到可用模型'
})

const options = computed(() => {
  const common = [
    {
      label: t('chat.copy'),
      key: 'copyText',
      icon: iconRender({ icon: 'ri:file-copy-2-line' }),
    },
    {
      label: t('common.delete'),
      key: 'delete',
      icon: iconRender({ icon: 'ri:delete-bin-line' }),
    },
  ]

  if (!props.inversion) {
    common.unshift({
      label: asRawText.value ? t('chat.preview') : t('chat.showRawText'),
      key: 'toggleRenderType',
      icon: iconRender({ icon: asRawText.value ? 'ic:outline-code-off' : 'ic:outline-code' }),
    })
  }

  return common
})

function handleSelect(key: 'copyText' | 'delete' | 'toggleRenderType') {
  switch (key) {
    case 'copyText':
      copyText({ text: props.text ?? '' })
      return
    case 'toggleRenderType':
      asRawText.value = !asRawText.value
      return
    case 'delete':
      emit('delete')
  }
}

function handleRegenerate() {
  messageRef.value?.scrollIntoView()
  emit('regenerate')
}
</script>

<template>
  <div
    ref="messageRef"
    class="flex w-full gap-4 pb-6"
    :class="[{ 'flex-row-reverse': inversion }]"
  >
    <div
      class="flex-shrink-0 pt-1"
      :class="[inversion ? 'ml-1' : 'mr-1']"
    >
      <AvatarComponent :image="inversion" />
    </div>
    <div class="min-w-0 flex-1 text-sm" :class="[inversion ? 'items-end' : 'items-start']">
      <div class="flex items-center gap-2 text-[11px] text-slate-400" :class="[inversion ? 'justify-end' : 'justify-start']">
        <span class="rounded-full border border-slate-200/80 bg-white/80 px-2.5 py-0.5 font-medium text-slate-500 dark:border-white/10 dark:bg-white/5 dark:text-slate-300">
          {{ senderLabel }}
        </span>
        <span>{{ dateTime }}</span>
      </div>
      <div
        class="mt-2 flex items-end gap-2"
        :class="[inversion ? 'flex-row-reverse' : 'flex-row']"
      >
        <TextComponent
          ref="textRef"
          :inversion="inversion"
          :error="error"
          :text="text"
          :loading="loading"
          :as-raw-text="asRawText"
        />
        <div class="flex flex-col gap-1">
          <button
            v-if="!inversion"
            class="inline-flex h-8 w-8 items-center justify-center rounded-full text-slate-400 transition hover:bg-white/80 hover:text-slate-700 dark:hover:bg-white/5 dark:hover:text-slate-100"
            @click="handleRegenerate"
          >
            <SvgIcon icon="ri:restart-line" />
          </button>
          <NDropdown
            :trigger="isMobile ? 'click' : 'hover'"
            :placement="!inversion ? 'right' : 'left'"
            :options="options"
            @select="handleSelect"
          >
            <button class="inline-flex h-8 w-8 items-center justify-center rounded-full text-slate-400 transition hover:bg-white/80 hover:text-slate-700 dark:hover:bg-white/5 dark:hover:text-slate-100">
              <SvgIcon icon="ri:more-2-fill" />
            </button>
          </NDropdown>
        </div>
      </div>
      <div v-if="!inversion && (trace || usage || budget)" class="mt-3 space-y-2">
        <div
          v-if="usage || budget"
          class="rounded-2xl border border-slate-200/70 bg-white/75 px-3 py-2 shadow-sm backdrop-blur dark:border-white/10 dark:bg-white/5"
        >
          <div class="flex items-center justify-between gap-3 text-[11px] font-medium text-slate-500 dark:text-slate-400">
            <span>Token 统计</span>
            <span>{{ tokenUsageLabel || answerSourceLabel }}</span>
          </div>
          <div class="mt-2 grid grid-cols-3 gap-2 text-[11px] text-slate-500 dark:text-slate-400">
            <div class="rounded-2xl border border-slate-200/70 bg-white/70 px-2.5 py-2 dark:border-white/10 dark:bg-white/5">
              <p class="text-[10px] uppercase tracking-[0.2em] text-slate-400">
                输入
              </p>
              <p class="mt-1 text-sm font-semibold text-slate-800 dark:text-slate-100">
                {{ formatTokenCount(tokenPromptCount) }}
              </p>
            </div>
            <div class="rounded-2xl border border-slate-200/70 bg-white/70 px-2.5 py-2 dark:border-white/10 dark:bg-white/5">
              <p class="text-[10px] uppercase tracking-[0.2em] text-slate-400">
                输出
              </p>
              <p class="mt-1 text-sm font-semibold text-slate-800 dark:text-slate-100">
                {{ formatTokenCount(tokenCompletionCount) }}
              </p>
            </div>
            <div class="rounded-2xl border border-slate-200/70 bg-white/70 px-2.5 py-2 dark:border-white/10 dark:bg-white/5">
              <p class="text-[10px] uppercase tracking-[0.2em] text-slate-400">
                总计
              </p>
              <p class="mt-1 text-sm font-semibold text-slate-800 dark:text-slate-100">
                {{ formatTokenCount(tokenTotalCount) }}
              </p>
            </div>
          </div>
          <div class="mt-2 flex items-center justify-between gap-3 text-[11px] text-slate-400 dark:text-slate-500">
            <span v-if="budget">剩余上下文 {{ formatTokenCount(contextRemainingTokens) }}</span>
            <span>{{ answerSourceDetail }}</span>
          </div>
          <p v-if="tokenUsageNote" class="mt-1 text-[11px] text-slate-400 dark:text-slate-500">
            {{ tokenUsageNote }}
          </p>
          <div class="mt-2 h-1.5 overflow-hidden rounded-full bg-slate-200/80 dark:bg-white/10">
            <div class="h-full rounded-full bg-gradient-to-r from-emerald-400 via-teal-400 to-cyan-400 transition-all duration-500" :style="contextBarStyle" />
          </div>
        </div>
        <div v-if="trace" class="flex flex-wrap gap-1.5 text-[11px] text-slate-500 dark:text-slate-400">
          <span class="rounded-full border border-slate-200/70 bg-white/70 px-2 py-0.5 font-medium dark:border-white/10 dark:bg-white/5">
            选定 · {{ selectedLabel }}
          </span>
          <span class="rounded-full border border-slate-200/70 bg-white/70 px-2 py-0.5 font-medium dark:border-white/10 dark:bg-white/5">
            实际 · {{ actualLabel }}
          </span>
          <span class="rounded-full border border-slate-200/70 bg-white/70 px-2 py-0.5 font-medium dark:border-white/10 dark:bg-white/5">
            来源 · {{ answerSourceLabel }}
          </span>
          <span v-if="trace.qaHitType" class="rounded-full border border-slate-200/70 bg-white/70 px-2 py-0.5 font-medium dark:border-white/10 dark:bg-white/5">
            命中层 · {{ trace.qaHitType }}
          </span>
          <span v-if="semanticModeLabel" class="rounded-full border border-slate-200/70 bg-white/70 px-2 py-0.5 font-medium dark:border-white/10 dark:bg-white/5">
            语义模式 · {{ semanticModeLabel }}
          </span>
          <span class="rounded-full border border-slate-200/70 bg-white/70 px-2 py-0.5 font-medium dark:border-white/10 dark:bg-white/5">
            模式 · {{ trace.answerMode }}
          </span>
        </div>
        <p v-if="fallbackNote" class="text-[11px] text-amber-500">
          {{ fallbackNote }}
        </p>
        <p v-if="semanticFallbackNote" class="text-[11px] text-amber-500">
          {{ semanticFallbackNote }}
        </p>
        <p v-if="trace?.retrievedHits?.length" class="text-[11px] text-slate-400 dark:text-slate-500">
          检索命中 · {{ trace.retrievedHits.length }}
        </p>
      </div>
    </div>
  </div>
</template>
