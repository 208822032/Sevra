<script lang="ts" setup>
import { computed } from 'vue'
import { NAvatar } from 'naive-ui'
import assistantAvatar from '@/assets/assistant-avatar.svg'
import { useUserStore } from '@/store'
import { isString } from '@/utils/is'
import defaultAvatar from '@/assets/avatar.jpg'

interface Props {
  image?: boolean
}
defineProps<Props>()

const userStore = useUserStore()

const avatar = computed(() => userStore.userInfo.avatar)
</script>

<template>
  <template v-if="image">
    <NAvatar v-if="isString(avatar) && avatar.length > 0" :src="avatar" :fallback-src="defaultAvatar" />
    <NAvatar v-else round :src="defaultAvatar" />
  </template>
  <span
    v-else
    class="inline-flex h-10 w-10 items-center justify-center overflow-hidden rounded-2xl border border-white/70 bg-white/80 p-1 shadow-sm dark:border-white/10 dark:bg-white/5"
  >
    <img :src="assistantAvatar" alt="知澜助手" class="h-full w-full rounded-[18px] object-cover" />
  </span>
</template>
