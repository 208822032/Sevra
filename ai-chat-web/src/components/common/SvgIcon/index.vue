<script setup lang='ts'>
import { computed, useAttrs } from 'vue'
import { Icon } from '@iconify/vue'

interface Props {
  icon?: string
}

const props = defineProps<Props>()

const attrs = useAttrs()

const iconName = computed(() => props.icon ?? '')

const bindAttrs = computed<{ class: string; style: string }>(() => ({
  class: (attrs.class as string) || '',
  style: (attrs.style as string) || '',
}))
</script>

<template>
  <Icon :icon="iconName" v-bind="bindAttrs" />
</template>
