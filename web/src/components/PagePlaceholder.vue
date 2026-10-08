<script setup lang="ts">
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
      <el-empty description="功能在后续阶段落地" :image-size="88" />
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
  border: 1px solid var(--app-border);
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
  background: var(--app-surface);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius);
}
</style>
