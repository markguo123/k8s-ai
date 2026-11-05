<script setup lang="ts">
import TechEmptyState from '@/components/TechEmptyState.vue'

defineProps<{
  code: string
  title: string
  desc: string
}>()
</script>

<template>
  <section class="page-placeholder">
    <div class="placeholder-head">
      <el-tag size="small" effect="plain">{{ code }}</el-tag>
      <div>
        <h2>{{ title }}</h2>
        <p>{{ desc }}</p>
      </div>
    </div>
    <div class="placeholder-panel">
      <!-- P7.6：与聊天页共用同一块空状态视觉，别再落回 el-empty 的灰底占位 -->
      <TechEmptyState title="功能在后续阶段落地" hint="该模块按四期计划在后续阶段交付" />
    </div>
  </section>
</template>

<style scoped>
.page-placeholder {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.placeholder-head {
  display: flex;
  align-items: flex-start;
  gap: 12px;
  background: var(--app-surface);
  backdrop-filter: blur(16px) saturate(120%);
  border: 1px solid var(--app-hairline);
  border-radius: var(--app-radius);
  padding: 16px;
}

.placeholder-head h2 {
  margin: 0 0 4px;
  font-size: 16px;
}

.placeholder-head p {
  margin: 0;
  color: var(--app-muted);
}

.placeholder-panel {
  display: flex;
  align-items: center;
  justify-content: center;
  min-height: 360px;
  /* 与聊天画布同档海拔：比侧栏亮一点 + 微弱内发光 */
  background: var(--app-canvas);
  box-shadow: var(--app-canvas-inset);
  border: 1px solid var(--app-hairline);
  border-radius: var(--app-radius);
}
</style>
