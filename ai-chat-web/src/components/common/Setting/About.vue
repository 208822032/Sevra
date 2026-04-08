<script setup lang='ts'>
import { computed, onMounted, ref } from 'vue'
import { NSpin } from 'naive-ui'
import { fetchChatConfig } from '@/api'
import pkg from '@/../package.json'
import { useAuthStore } from '@/store'
import { formatModelOptionLabel, formatProviderLabel } from '@/utils/modelCatalog'

const authStore = useAuthStore()

const loading = ref(false)

const config = ref<Chat.ChatConfigResponse>()

const isChatGPTAPI = computed<boolean>(() => !!authStore.isChatGPTAPI)

const providerCount = computed(() => config.value?.providers?.length ?? 0)

async function fetchConfig() {
  try {
    loading.value = true
    const { data } = await fetchChatConfig<Chat.ChatConfigResponse>()
    config.value = data
  }
  finally {
    loading.value = false
  }
}

onMounted(() => {
  fetchConfig()
})
</script>

<template>
  <NSpin :show="loading">
    <div class="p-4 space-y-4">
      <h2 class="text-xl font-bold">
        Version - {{ pkg.version }}
      </h2>
      <div class="p-2 space-y-2 rounded-md bg-neutral-100 dark:bg-neutral-700">
        <p>
          此项目开源于
          <a
            class="text-blue-600 dark:text-blue-500"
            href="https://github.com/Chanzhaoyu/chatgpt-web"
            target="_blank"
          >
            Github
          </a>
          ，免费且基于 MIT 协议，没有任何形式的付费行为！
        </p>
        <p>
          如果你觉得此项目对你有帮助，请在 Github 帮我点个 Star 或者给予一点赞助，谢谢！
        </p>
      </div>
      <p>{{ $t("setting.api") }}：{{ config?.apiModel ?? '-' }}</p>
      <p>当前 Provider：{{ config?.selectedProvider ?? '-' }}</p>
      <p>当前 Model：{{ config?.selectedModel ?? '-' }}</p>
      <p>可用 Provider 数：{{ providerCount }}</p>
      <p v-if="isChatGPTAPI">
        {{ $t("setting.balance") }}：{{ config?.balance ?? '-' }}
      </p>
      <p v-if="!isChatGPTAPI">
        {{ $t("setting.reverseProxy") }}：{{ config?.reverseProxy ?? '-' }}
      </p>
      <p>{{ $t("setting.timeout") }}：{{ config?.timeoutMs ?? '-' }}</p>
      <p>{{ $t("setting.socks") }}：{{ config?.socksProxy ?? '-' }}</p>
      <p>{{ $t("setting.httpsProxy") }}：{{ config?.httpsProxy ?? '-' }}</p>
      <div v-if="config?.providers?.length" class="space-y-2">
        <div
          v-for="provider in config.providers"
          :key="provider.provider"
          class="p-3 rounded-md bg-white/60 dark:bg-black/20"
        >
          <p class="flex flex-wrap items-center gap-2 font-semibold">
            <span>{{ formatProviderLabel(provider) }}</span>
            <span class="text-xs text-neutral-500 dark:text-neutral-400">
              {{ provider.provider }}
            </span>
          </p>
          <p class="text-sm text-neutral-500 dark:text-neutral-400">
            状态：{{ provider.enabled ? '启用' : '停用' }}{{ provider.isDefault ? ' · 默认' : '' }}
          </p>
          <p class="text-sm text-neutral-500 dark:text-neutral-400">
            Default：{{ provider.defaultModel || '-' }}
          </p>
          <p class="text-sm text-neutral-500 dark:text-neutral-400">
            Models：{{ provider.models?.length ? provider.models.map(model => formatModelOptionLabel(model)).join(', ') : '-' }}
          </p>
        </div>
      </div>
    </div>
  </NSpin>
</template>
