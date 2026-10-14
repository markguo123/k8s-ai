<script setup lang="ts">
/**
 * P2.7 AgentStepper：单回合内的过程步进器，每轮重置。
 * 四步语义对齐 §5.3：思考 → 工具 → 观察 → 报告。
 */

export type StepState = 'done' | 'active' | 'pending' | 'error' | 'idle'

export interface AgentStep {
  key: string
  label: string
  state: StepState
}

defineProps<{
  steps: AgentStep[]
}>()
</script>

<template>
  <ol class="stepper" aria-label="回合过程">
    <li
      v-for="(step, index) in steps"
      :key="step.key"
      class="step"
      :class="step.state"
    >
      <span class="step-dot">
        <svg v-if="step.state === 'done'" viewBox="0 0 12 12" class="step-check" aria-hidden="true">
          <path d="M2.5 6.2 5 8.6l4.5-5.4" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" />
        </svg>
        <template v-else-if="step.state === 'active'"><span class="step-pulse"></span></template>
        <template v-else-if="step.state === 'error'">!</template>
        <template v-else>{{ index + 1 }}</template>
      </span>
      <span class="step-label">{{ step.label }}</span>
    </li>
  </ol>
</template>

<style scoped>
.stepper {
  display: flex;
  align-items: center;
  gap: 6px;
  margin: 0;
  padding: 8px 4px 0;
  list-style: none;
}

.step {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
  color: var(--app-muted);
  font-size: 12px;
}

.step + .step::before {
  content: '';
  display: block;
  width: 18px;
  height: 1px;
  margin: 0 6px;
  background: var(--app-border);
}

.step-dot {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 18px;
  height: 18px;
  border-radius: 50%;
  flex: none;
  background: var(--app-bg);
  border: 1px solid var(--app-border);
  font-size: 11px;
  line-height: 1;
  font-variant-numeric: tabular-nums;
}

.step-label {
  white-space: nowrap;
}

.step.done {
  color: var(--app-success, #22c55e);
}

.step.done .step-dot {
  color: #fff;
  background: var(--app-success, #22c55e);
  border-color: var(--app-success, #22c55e);
}

.step-check {
  width: 10px;
  height: 10px;
}

.step.active {
  color: var(--app-accent);
  font-weight: 600;
}

.step.active .step-dot {
  border-color: var(--app-accent);
  color: var(--app-accent);
}

.step-pulse {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background: var(--app-accent);
  animation: stepper-pulse 1.1s ease-in-out infinite;
}

.step.error {
  color: var(--app-danger, #ef4444);
}

.step.error .step-dot {
  color: #fff;
  background: var(--app-danger, #ef4444);
  border-color: var(--app-danger, #ef4444);
}

.step.pending .step-label {
  opacity: 0.6;
}

@keyframes stepper-pulse {
  0%,
  100% {
    opacity: 1;
    transform: scale(1);
  }
  50% {
    opacity: 0.35;
    transform: scale(0.75);
  }
}
</style>
