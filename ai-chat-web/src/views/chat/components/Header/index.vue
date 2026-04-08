<script lang="ts" setup>
import { computed, nextTick } from 'vue'
import { HoverButton, SvgIcon } from '@/components/common'
import assistantAvatar from '@/assets/assistant-avatar.svg'
import { APP_NAME } from '@/constants/branding'
import { useAppStore, useChatStore } from '@/store'

interface Props {
  usingContext: boolean
}

interface Emit {
  (ev: 'export'): void
  (ev: 'toggleUsingContext'): void
}

defineProps<Props>()

const emit = defineEmits<Emit>()

const appStore = useAppStore()
const chatStore = useChatStore()

const collapsed = computed(() => appStore.siderCollapsed)
const currentChatHistory = computed(() => chatStore.getChatHistoryByCurrentActive)

function handleUpdateCollapsed() {
  appStore.setSiderCollapsed(!collapsed.value)
}

function onScrollToTop() {
  const scrollRef = document.querySelector('#scrollRef')
  if (scrollRef)
    nextTick(() => scrollRef.scrollTop = 0)
}

function handleExport() {
  emit('export')
}

function toggleUsingContext() {
  emit('toggleUsingContext')
}
</script>

<template>
  <header
    class="sticky top-0 left-0 right-0 z-30 border-b border-white/70 bg-white/85 backdrop-blur-xl dark:border-white/10 dark:bg-[#0b1020]/80"
  >
    <div class="relative flex min-w-0 items-center justify-between overflow-hidden px-2 py-2.5">
      <div class="flex items-center">
        <button
          class="flex h-11 w-11 items-center justify-center rounded-full text-slate-500 transition hover:bg-slate-100 hover:text-slate-900 dark:text-slate-300 dark:hover:bg-white/5 dark:hover:text-white"
          @click="handleUpdateCollapsed"
        >
          <SvgIcon v-if="collapsed" class="text-2xl" icon="ri:align-justify" />
          <SvgIcon v-else class="text-2xl" icon="ri:align-right" />
        </button>
      </div>
      <div class="flex min-w-0 flex-1 items-center gap-3 px-3" @dblclick="onScrollToTop">
        <div class="flex h-10 w-10 shrink-0 items-center justify-center overflow-hidden rounded-2xl border border-white/70 bg-white/80 p-1 shadow-sm dark:border-white/10 dark:bg-white/5">
          <img :src="assistantAvatar" alt="知澜助手" class="h-full w-full rounded-[14px] object-cover" />
        </div>
        <div class="min-w-0">
          <p class="text-[11px] font-semibold uppercase tracking-[0.24em] text-slate-400">
            {{ APP_NAME }}
          </p>
          <h1 class="truncate text-sm font-semibold text-slate-900 dark:text-white">
            {{ currentChatHistory?.title ?? '新对话' }}
          </h1>
        </div>
      </div>
      <div class="flex items-center space-x-2">
        <HoverButton @click="toggleUsingContext">
          <span class="text-xl" :class="{ 'text-[#4b9e5f]': usingContext, 'text-[#a8071a]': !usingContext }">
            <SvgIcon icon="ri:chat-history-line" />
          </span>
        </HoverButton>
        <HoverButton @click="handleExport">
          <span class="text-xl text-[#4f555e] dark:text-white">
            <SvgIcon icon="ri:download-2-line" />
          </span>
        </HoverButton>
      </div>
    </div>
  </header>
</template>
