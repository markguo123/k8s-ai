<script setup lang="ts">
import { computed } from 'vue'

import { statusPillClass, tlLabel, type StatusTone } from './constants'

const props = withDefaults(
  defineProps<{
    events: unknown[]
    label?: string
  }>(),
  {
    label: '',
  },
)

const COLUMNS = ['lastTimestamp', 'type', 'reason', 'message']

const records = computed<Record<string, unknown>[]>(() =>
  (props.events ?? []).filter(
    (event): event is Record<string, unknown> => !!event && typeof event === 'object' && !Array.isArray(event),
  ),
)

const columns = computed<string[]>(() =>
  COLUMNS.filter((column) => records.value.some((record) => record[column] !== undefined && record[column] !== null)),
)

function text(value: unknown): string {
  if (value === null || value === undefined || value === '') return ''
  return typeof value === 'object' ? JSON.stringify(value).slice(0, 120) : String(value)
}

function tone(value: unknown): StatusTone | null {
  return statusPillClass(value)
}
</script>

<template>
  <div v-if="records.length" class="ev-wrap">
    <div v-if="label" class="ev-sub">{{ label }}</div>
    <div class="ev-scroll">
      <table class="ev-table">
        <thead>
          <tr>
            <th v-for="column in columns" :key="column">{{ tlLabel(column) }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="(record, index) in records" :key="index">
            <td v-for="column in columns" :key="column" class="ev-cell">
              <span v-if="column === 'type'" class="st-pill" :class="tone(record[column])">
                {{ text(record[column]) || 'Normal' }}
              </span>
              <span v-else class="ev-value">{{ text(record[column]) }}</span>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>

<style scoped>
.ev-wrap {
  display: flex;
  flex-direction: column;
  gap: 6px;
  margin-top: 8px;
}

.ev-sub {
  font-size: 11px;
  font-weight: 600;
  color: var(--app-muted);
}

.ev-scroll {
  overflow: auto;
  border: 1px solid var(--app-border);
  border-radius: 6px;
}

.ev-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 12px;
}

.ev-table th,
.ev-table td {
  padding: 6px 10px;
  border-bottom: 1px solid var(--app-border);
  text-align: left;
  vertical-align: top;
}

.ev-table th {
  position: sticky;
  top: 0;
  background: var(--app-header-bg, var(--app-bg));
  color: var(--app-muted);
  font-weight: 600;
  white-space: nowrap;
}

.ev-table tbody tr:last-child th,
.ev-table tbody tr:last-child td {
  border-bottom: none;
}

.ev-table tbody tr:hover td {
  background: var(--app-accent-soft);
}

.ev-cell {
  min-width: 80px;
}

.ev-value {
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
