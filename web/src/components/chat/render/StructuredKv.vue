<script setup lang="ts">
import { computed } from 'vue'

import { TL_KV_FIELDS, statusPillClass, tlLabel, type StatusTone } from './constants'

const props = defineProps<{
  data: Record<string, unknown>
}>()

interface KvRow {
  key: string
  value: string
  tone: StatusTone | null
}

const rows = computed<KvRow[]>(() => {
  const source = props.data ?? {}
  return TL_KV_FIELDS.filter(
    (key) => source[key] !== undefined && source[key] !== null && typeof source[key] !== 'object',
  ).map((key) => {
    const raw = source[key]
    let tone: StatusTone | null = null
    if (key === 'phase' || key === 'status' || key === 'state') {
      tone = statusPillClass(raw)
    }
    return { key, value: String(raw), tone }
  })
})
</script>

<template>
  <div v-if="rows.length" class="kv-wrap">
    <table class="kv-table">
      <tbody>
        <tr v-for="row in rows" :key="row.key">
          <th class="kv-key">{{ tlLabel(row.key) }}</th>
          <td class="kv-value">
            <span v-if="row.tone" class="st-pill" :class="row.tone">{{ row.value }}</span>
            <template v-else>{{ row.value }}</template>
          </td>
        </tr>
      </tbody>
    </table>
  </div>
</template>

<style scoped>
.kv-wrap {
  overflow: auto;
  border: 1px solid var(--app-border);
  border-radius: 6px;
}

.kv-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 12px;
}

.kv-table th,
.kv-table td {
  padding: 6px 10px;
  border-bottom: 1px solid var(--app-border);
  text-align: left;
  vertical-align: top;
}

.kv-table tbody tr:last-child th,
.kv-table tbody tr:last-child td {
  border-bottom: none;
}

.kv-table tr:hover td {
  background: var(--app-accent-soft);
}

.kv-key {
  width: 130px;
  color: var(--app-muted);
  font-weight: 600;
  white-space: nowrap;
}

.kv-value {
  color: var(--app-text);
  word-break: break-word;
}

.st-pill {
  display: inline-block;
  padding: 1px 8px;
  border-radius: 999px;
  font-size: 11px;
  white-space: nowrap;
}

.st-pill.normal {
  color: var(--app-muted);
  background: var(--app-bg);
}

.st-pill.ok {
  color: var(--app-success, #22c55e);
  background: rgba(34, 197, 94, 0.14);
}

.st-pill.pending {
  color: var(--app-warning, #f59e0b);
  background: rgba(245, 158, 11, 0.14);
}

.st-pill.error {
  color: var(--app-danger, #ef4444);
  background: rgba(239, 68, 68, 0.14);
}

.st-pill.warning {
  color: var(--app-warning, #f59e0b);
  background: rgba(245, 158, 11, 0.14);
}
</style>
