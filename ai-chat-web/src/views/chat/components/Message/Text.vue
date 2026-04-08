<script lang="ts" setup>
import { computed, ref } from 'vue'
import MarkdownIt from 'markdown-it'
import mdKatex from '@traptitech/markdown-it-katex'
import mila from 'markdown-it-link-attributes'
import hljs from 'highlight.js'
import { useBasicLayout } from '@/hooks/useBasicLayout'
import { t } from '@/locales'

interface Props {
  inversion?: boolean
  error?: boolean
  text?: string
  loading?: boolean
  asRawText?: boolean
}

const props = defineProps<Props>()

const { isMobile } = useBasicLayout()

const textRef = ref<HTMLElement>()

const mdi = new MarkdownIt({
  linkify: true,
  highlight(code, language) {
    const validLang = !!(language && hljs.getLanguage(language))
    if (validLang) {
      const lang = language ?? ''
      return highlightBlock(hljs.highlight(code, { language: lang }).value, lang)
    }
    return highlightBlock(hljs.highlightAuto(code).value, '')
  },
})

mdi.use(mila, { attrs: { target: '_blank', rel: 'noopener' } })
mdi.use(mdKatex, { blockClass: 'katexmath-block rounded-md p-[10px]', errorColor: ' #cc0000' })

const wrapClass = computed(() => {
  return [
    'text-wrap',
    'min-w-[20px]',
    'rounded-[22px]',
    'border',
    'shadow-[0_10px_30px_rgba(15,23,42,0.05)]',
    'backdrop-blur',
    isMobile.value ? 'px-3 py-2.5' : 'px-4 py-3',
    props.inversion
      ? 'message-request border-emerald-200/70 bg-emerald-50/90 text-slate-900 dark:border-emerald-400/20 dark:bg-emerald-500/10 dark:text-slate-100'
      : 'message-reply border-slate-200/80 bg-white/90 text-slate-900 dark:border-white/10 dark:bg-white/5 dark:text-slate-100',
    { 'text-red-500': props.error },
  ]
})

const text = computed(() => {
  const value = props.text ?? ''
  if (!props.asRawText)
    return mdi.render(value)
  return value
})

function highlightBlock(str: string, lang?: string) {
  return `<pre class="code-block-wrapper"><div class="code-block-header"><span class="code-block-header__lang">${lang}</span><span class="code-block-header__copy">${t('chat.copyCode')}</span></div><code class="hljs code-block-body ${lang}">${str}</code></pre>`
}

defineExpose({ textRef })
</script>

<template>
  <div :class="wrapClass">
    <template v-if="loading">
      <span class="dark:text-white block h-[20px] w-[4px] animate-blink" />
    </template>
    <template v-else>
      <div ref="textRef" class="break-words leading-7">
        <div v-if="!inversion">
          <div v-if="!asRawText" class="markdown-body" v-html="text" />
          <div v-else class="whitespace-pre-wrap" v-text="text" />
        </div>
        <div v-else class="whitespace-pre-wrap" v-text="text" />
      </div>
    </template>
  </div>
</template>

<style lang="less">
@import url(./style.less);
</style>
