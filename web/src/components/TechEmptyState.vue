<script setup lang="ts">
/**
 * P7.6 科技感空状态：雷达扫描 + 节点连线插画（纯 SVG/CSS，不引粒子库）。
 *
 * 同一套"空"的视觉语言三处共用，替掉各页各写一块灰底占位：
 * - 会话列表（`compact`）：暂无会话
 * - 聊天画布：未选择会话
 * - 消息时间线：会话已建但还没有消息
 */
withDefaults(
  defineProps<{
    /** 主标题：一句话说明"现在没有内容"。 */
    title: string
    /** 引导文案：告诉用户下一步做什么。 */
    hint?: string
    /** 窄容器（会话列表）下用小尺寸插画。 */
    compact?: boolean
  }>(),
  { hint: '', compact: false },
)
</script>

<template>
  <div class="tech-empty" :class="{ 'is-compact': compact }" role="status">
    <svg class="tech-empty-art" viewBox="0 0 220 140" aria-hidden="true" focusable="false">
      <g class="rings" fill="none" stroke="currentColor" stroke-width="1">
        <circle cx="110" cy="70" r="16" opacity="0.3" />
        <circle cx="110" cy="70" r="32" opacity="0.22" />
        <circle cx="110" cy="70" r="48" opacity="0.14" />
        <path d="M110 14v112M42 70h136" opacity="0.12" />
        <path d="M110 18v6M110 116v6M58 70h6M156 70h6" stroke-width="1.4" opacity="0.34" />
      </g>
      <g class="sweep">
        <path d="M110 70 110 22A48 48 0 0 1 151.6 46Z" fill="currentColor" opacity="0.14" />
        <path d="M110 70 110 22" stroke="currentColor" stroke-width="1.6" opacity="0.6" />
      </g>
      <g class="links" fill="none" stroke="currentColor" stroke-width="1.2" opacity="0.45">
        <path d="M110 70 158 42M110 70 66 44M110 70 150 108M110 70 62 100" />
        <path d="M158 42 62 100" stroke-dasharray="3 4" />
      </g>
      <g class="nodes" fill="currentColor">
        <circle cx="158" cy="42" r="4" />
        <circle class="node-alt" cx="66" cy="44" r="3.2" />
        <circle class="node-alt" cx="150" cy="108" r="3.2" />
        <circle cx="62" cy="100" r="4" />
        <circle class="node-core" cx="110" cy="70" r="5" />
      </g>
    </svg>
    <p class="tech-empty-title">{{ title }}</p>
    <p v-if="hint" class="tech-empty-hint">{{ hint }}</p>
    <div v-if="$slots.default" class="tech-empty-actions">
      <slot />
    </div>
  </div>
</template>

<style scoped>
.tech-empty {
  position: relative;
  display: flex;
  flex: 1;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 10px;
  min-height: 0;
  padding: 32px 16px;
  text-align: center;
}

/* 插画背后的极弱主色光晕：空状态不再"塌成一块黑" */
.tech-empty::before {
  content: '';
  position: absolute;
  width: 320px;
  height: 320px;
  border-radius: 50%;
  background: radial-gradient(circle, rgba(99, 102, 241, 0.1), transparent 66%);
  pointer-events: none;
}

.tech-empty-art {
  position: relative;
  width: 216px;
  height: 138px;
  color: var(--app-accent);
  opacity: 0.9;
}

.is-compact .tech-empty-art {
  width: 132px;
  height: 84px;
}

.is-compact::before {
  width: 200px;
  height: 200px;
}

.sweep {
  transform-origin: 110px 70px;
  animation: radarSweep 4.6s linear infinite;
}

.node-alt {
  fill: var(--app-accent-2);
}

.node-core {
  fill: var(--app-accent-2);
  transform-box: fill-box;
  transform-origin: center;
  animation: radarPing 2.4s ease-out infinite;
}

@keyframes radarSweep {
  to {
    transform: rotate(360deg);
  }
}

/* 与登录页 .status i 同款的呼吸扩散点 */
@keyframes radarPing {
  0% {
    transform: scale(1);
    opacity: 1;
  }
  70%,
  100% {
    transform: scale(2.6);
    opacity: 0;
  }
}

.tech-empty-title {
  position: relative;
  margin: 0;
  color: var(--app-text);
  font-size: 14px;
  font-weight: 600;
}

.is-compact .tech-empty-title {
  font-size: 13px;
}

.tech-empty-hint {
  position: relative;
  margin: 0;
  /* 宽一点：中文按 440px 断行更自然，避免"工具"这类词被拆到两行 */
  max-width: 440px;
  color: var(--app-muted);
  font-size: 12.5px;
  line-height: 1.6;
}

.is-compact .tech-empty-hint {
  font-size: 12px;
}

.tech-empty-actions {
  position: relative;
  display: flex;
  gap: 8px;
  margin-top: 4px;
}

@media (prefers-reduced-motion: reduce) {
  .sweep,
  .node-core {
    animation: none;
  }
}
</style>
