<script setup lang="ts">
import { ref, watch } from 'vue'
import { marked } from 'marked'
import DOMPurify from 'dompurify'

import copyText from '@/composables/useClipboard'

/**
 * P2.8 最终报告/思考的 Markdown 渲染（§7 管线）。
 * raw → marked.parse → DOMPurify 白名单清洗 → 受控容器 innerHTML。
 * 代码块统一加「语言标签 + 复制」，链接强制新标签打开且不携带 opener。
 */

const props = withDefaults(
  defineProps<{
    text: string
  }>(),
  {
    text: '',
  },
)

/** 代码片段缓存：DOMPurify 不允许注入按钮，改用事件委托在受控容器内取值。 */
const snippets = ref<string[]>([])
let snippetCount = 0

let hookRegistered = false
function ensureHook(): void {
  if (hookRegistered) {
    return
  }
  hookRegistered = true
  DOMPurify.addHook('afterSanitizeAttributes', (node) => {
    if (node.tagName === 'A') {
      node.setAttribute('target', '_blank')
      node.setAttribute('rel', 'noopener noreferrer')
    }
  })
}

function render(): string {
  ensureHook()
  const raw = props.text ?? ''
  // 走 DOMPurify 默认 HTML 白名单：该白名单含 class，代码块语言标签依赖它。
  // 注意不要打开 svg / svgFilters —— 报告正文来自集群数据与模型输出，属不可信内容。
  const html = DOMPurify.sanitize(marked.parse(raw) as string)
  const doc = new DOMParser().parseFromString(`<div id="__mdroot">${html}</div>`, 'text/html')
  const root = doc.getElementById('__mdroot')
  if (root) {
    const pres = root.querySelectorAll('pre')
    pres.forEach((pre) => {
      const code = pre.querySelector('code')
      const language =
        (code
          ?.getAttribute('class')
          ?.match(/(?:^|\s)language-([\w-]+)/)?.[1]) ||
        'code'
      const content = code?.textContent ?? pre.textContent ?? ''
      const wrapper = doc.createElement('div')
      wrapper.setAttribute('class', 'md-code-wrap')
      const head = doc.createElement('div')
      head.setAttribute('class', 'md-code-head')
      const label = doc.createElement('span')
      label.textContent = language
      const copy = doc.createElement('button')
      copy.type = 'button'
      copy.setAttribute('class', 'md-code-copy')
      copy.setAttribute('data-idx', String(snippetCount))
      copy.textContent = '复制'
      head.append(label, copy)
      // 先用 wrapper 顶替 pre 在文档中的位置，再把 pre 收进 wrapper：
      // 顺序反了会变成「把父节点塞进自己的子节点」，触发 HierarchyRequestError。
      pre.replaceWith(wrapper)
      wrapper.append(head, pre)
      snippets.value.push(content)
      snippetCount += 1
    })
  }
  return root ? root.innerHTML : html
}

const rendered = ref(render())

watch(
  () => props.text,
  () => {
    snippets.value = []
    snippetCount = 0
    rendered.value = render()
  },
)

function onContainerClick(event: MouseEvent): void {
  const target = event.target as HTMLElement
  const copy = target.closest('button.md-code-copy') as HTMLElement | null
  if (!copy) {
    return
  }
  const idx = Number(copy.dataset.idx)
  const snippet = Number.isFinite(idx) ? snippets.value[idx] : undefined
  if (typeof snippet === 'string' && snippet.length > 0) {
    copyText(snippet, '代码已复制')
  }
}
</script>

<template>
  <div class="md" @click="onContainerClick">
    <!-- 受控容器：渲染结果已由 DOMPurify 清洗，仅此组件内允许 v-html。 -->
    <div v-if="rendered" class="md-body" v-html="rendered"></div>
    <p v-else class="md-empty">（无内容）</p>
  </div>
</template>

<style scoped>
.md {
  min-width: 0;
  max-width: 100%;
}

.md-body {
  overflow-wrap: break-word;
  line-height: 1.65;
}

.md-body :deep(h1),
.md-body :deep(h2),
.md-body :deep(h3),
.md-body :deep(h4),
.md-body :deep(h5),
.md-body :deep(h6) {
  margin: 0.9em 0 0.45em;
  line-height: 1.3;
  color: var(--app-strong, var(--app-text));
}

.md-body :deep(h1) {
  font-size: 1.35em;
}

.md-body :deep(h2) {
  font-size: 1.2em;
}

.md-body :deep(h3) {
  font-size: 1.08em;
}

.md-body :deep(p) {
  margin: 0.5em 0;
}

.md-body :deep(ul),
.md-body :deep(ol) {
  margin: 0.5em 0;
  padding-left: 1.4em;
}

.md-body :deep(li) {
  margin: 0.2em 0;
}

.md-body :deep(a) {
  color: var(--app-accent);
  text-decoration: none;
}

.md-body :deep(a:hover) {
  text-decoration: underline;
}

.md-body :deep(code) {
  padding: 0.1em 0.35em;
  border-radius: 4px;
  background: var(--app-code-inline-bg, rgba(255, 255, 255, 0.08));
  font-family: 'JetBrains Mono', Consolas, monospace;
  font-size: 0.92em;
}

.md-body :deep(pre) {
  overflow: auto;
  margin: 0.6em 0;
  padding: 10px 12px;
  border-radius: 6px;
  background: var(--app-code-bg, #0f172a);
  color: #e2e8f0;
  font-family: 'JetBrains Mono', Consolas, monospace;
  font-size: 12px;
  line-height: 1.5;
  white-space: pre-wrap;
  word-break: break-word;
}

.md-body :deep(pre code) {
  padding: 0;
  background: transparent;
  font-size: inherit;
}

.md-body :deep(blockquote) {
  margin: 0.6em 0;
  padding: 4px 12px;
  border-left: 3px solid var(--app-accent);
  border-radius: 0 6px 6px 0;
  background: var(--app-accent-soft);
  color: var(--app-muted);
}

.md-body :deep(table) {
  width: 100%;
  margin: 0.6em 0;
  border-collapse: collapse;
  font-size: 12px;
}

.md-body :deep(th),
.md-body :deep(td) {
  padding: 6px 10px;
  border: 1px solid var(--app-border);
  text-align: left;
}

.md-body :deep(th) {
  background: var(--app-header-bg, var(--app-bg));
  color: var(--app-muted);
  font-weight: 600;
}

.md-body :deep(img) {
  max-width: 100%;
  border-radius: 6px;
}

.md-code-wrap {
  overflow: hidden;
  margin: 0.6em 0;
  border: 1px solid var(--app-border);
  border-radius: 8px;
}

.md-code-wrap :deep(pre) {
  margin: 0;
  border-radius: 0;
  max-height: 420px;
}

.md-code-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 5px 10px;
  border-bottom: 1px solid rgba(255, 255, 255, 0.08);
  background: var(--app-code-head-bg, rgba(255, 255, 255, 0.04));
  font-family: 'JetBrains Mono', Consolas, monospace;
  font-size: 11px;
}

.md-code-head span {
  color: #94a3b8;
}

.md-code-copy {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 2px 8px;
  border: none;
  border-radius: 4px;
  background: transparent;
  color: #cbd5e1;
  font-family: inherit;
  font-size: 11px;
  cursor: pointer;
}

.md-code-copy:hover {
  background: rgba(255, 255, 255, 0.1);
}

.md-empty {
  margin: 0;
  color: var(--app-muted);
  font-size: 12px;
}
</style>
