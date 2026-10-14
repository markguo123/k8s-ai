<script setup lang="ts">
import { computed } from 'vue'

import { TL_LIST_COLUMNS, statusPillClass, tlLabel, type StatusTone } from './constants'

const props = withDefaults(
  defineProps<{
    items: unknown[]
    /** 资源 kind，用于挑选 TL_LIST_COLUMNS 列配置。 */
    kind?: string
    /** 表格上方小标题，如 events / conditions。 */
    label?: string
  }>(),
  {
    kind: '',
    label: '',
  },
)

/** 列探测优先序（对齐老 UI 的启发式列池）。 */
const FALLBACK_COLUMN_ORDER = [
  'kind',
  'name',
  'namespace',
  'phase',
  'status',
  'state',
  'type',
  'reason',
  'count',
  'restartCount',
]

function cellText(value: unknown): string {
  if (value === null || value === undefined || value === '') {
    return ''
  }
  if (typeof value === 'object') {
    try {
      return JSON.stringify(value).slice(0, 120)
    } catch {
      return '…'
    }
  }
  return String(value)
}

function cellTone(key: string, value: unknown): StatusTone | null {
  if (key === 'phase' || key === 'status' || key === 'state') {
    return statusPillClass(value)
  }
  if (key === 'ready' && (value === true || value === false)) {
    return value ? 'ok' : 'pending'
  }
  return null
}

const columns = computed<string[]>(() => {
  const records = (props.items ?? []).slice(0, 6).filter(
    (item): item is Record<string, unknown> =>
      !!item && typeof item === 'object' && !Array.isArray(item),
  )
  const total = props.items.length

  const configured = props.kind ? TL_LIST_COLUMNS[props.kind] : undefined
  if (configured) {
    return configured
  }

  if (records.length > 0) {
    const pool: Record<string, number> = {}
    for (const record of records) {
      for (const key of Object.keys(record)) {
        pool[key] = (pool[key] || 0) + 1
      }
    }
    const detected = FALLBACK_COLUMN_ORDER.filter((key) => pool[key] >= total * 0.5)
    if (detected.length > 0) {
      return detected
    }
    return Object.keys(pool).slice(0, 5)
  }

  // 非对象元素（字符串列表）降级为单列。
  return ['value']
})
</script>

<template>
  <div class="st-table-wrap" :class="{ labeled: !!label }">
    <div v-if="label" class="st-sub">{{ label }}</div>
    <div v-if="items.length" class="st-table-scroll">
      <table class="st-table">
        <thead>
          <tr>
            <th v-for="column in columns" :key="column">{{ tlLabel(column) }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="(item, rowIndex) in items" :key="rowIndex">
            <td v-for="column in columns" :key="column" class="st-cell">
              <template v-if="item && typeof item === 'object' && !Array.isArray(item)">
                <span
                  v-if="cellTone(column, (item as Record<string, unknown>)[column])"
                  class="st-pill"
                  :class="cellTone(column, (item as Record<string, unknown>)[column])"
                >
                  {{
                    (item as Record<string, unknown>)[column] === true ||
                    (item as Record<string, unknown>)[column] === false
                      ? (item as Record<string, unknown>)[column]
                        ? 'True'
                        : 'False'
                      : cellText((item as Record<string, unknown>)[column])
                  }}
                </span>
                <span v-else class="st-value">{{ cellText((item as Record<string, unknown>)[column]) }}</span>
              </template>
              <span v-else class="st-value">{{ cellText(item) }}</span>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
    <div v-else class="st-empty">（无数据）</div>
  </div>
</template>

<style scoped>
.st-table-wrap {
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.st-table-wrap.labeled {
  margin-top: 8px;
}

.st-sub {
  font-size: 11px;
  font-weight: 600;
  color: var(--app-muted);
}

.st-table-scroll {
  overflow: auto;
  border: 1px solid var(--app-border);
  border-radius: 6px;
}

.st-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 12px;
}

.st-table th,
.st-table td {
  padding: 6px 10px;
  border-bottom: 1px solid var(--app-border);
  text-align: left;
  vertical-align: top;
}

.st-table th {
  position: sticky;
  top: 0;
  background: var(--app-header-bg, var(--app-bg));
  color: var(--app-muted);
  font-weight: 600;
  white-space: nowrap;
}

.st-table tbody tr:last-child th,
.st-table tbody tr:last-child td {
  border-bottom: none;
}

.st-table tbody tr:hover td {
  background: var(--app-accent-soft);
}

.st-cell {
  min-width: 80px;
}

.st-value {
  color: var(--app-text);
  word-break: break-word;
}

.st-empty {
  padding: 8px;
  color: var(--app-muted);
  font-size: 12px;
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
