<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { Document, MagicStick, Refresh, UploadFilled, Upload } from '@element-plus/icons-vue'

import {
  describeSkillError,
  importProceduralDocs,
  importProceduralZip,
  importSkills,
  listSkills,
  setSkillEnabled,
} from '@/api/skills'
import type { Skill } from '@/api/types'
import { useAuthStore } from '@/stores/auth'
import { hasPermission, PERM, permissionsOf } from '@/utils/permission'

const auth = useAuthStore()
const skills = ref<Skill[]>([])
const loading = ref(false)
const importing = ref(false)
const yamlText = ref('')
const mdText = ref('')
const zipFile = ref<File | null>(null)
const zipPicked = ref(false)
const zipInputRef = ref<HTMLInputElement | null>(null)

/** 导入模式（P3.8）：structured 声明式 YAML / procedural 文本 / procedural zip 包。 */
type ImportMode = 'structured' | 'procedural' | 'zip'
const importMode = ref<ImportMode>('structured')

/** 只有 skill.manage 才能导入与启停；查看权由 skill.read 控制（后端独立强校验）。 */
const canManage = computed(() => hasPermission(permissionsOf(auth.role), PERM.skillManage))

const totalCount = computed(() => skills.value.length)
const enabledCount = computed(() => skills.value.filter((s) => s.enabled).length)
const disabledCount = computed(() => totalCount.value - enabledCount.value)
const totalHits = computed(() => skills.value.reduce((sum, s) => sum + (Number(s.hitCount) || 0), 0))

function sourceType(source: string): 'info' | 'success' | 'warning' | 'danger' {
  switch (source) {
    case 'builtin':
      return 'info'
    case 'custom':
      return 'success'
    case 'third-party':
      return 'warning'
    default:
      return 'danger'
  }
}

function sourceText(source: string): string {
  switch (source) {
    case 'builtin':
      return '内置'
    case 'custom':
      return '自定义'
    case 'third-party':
      return '第三方'
    default:
      return source || '未知'
  }
}

function formatTime(value?: string): string {
  if (!value) {
    return '—'
  }
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) {
    return value
  }
  return date.toLocaleString()
}

function typeClass(type?: string): 'info' | 'success' | 'warning' | 'danger' {
  return type === 'procedural' ? 'warning' : type === 'third-party' ? 'danger' : 'info'
}

function typeText(type?: string): string {
  if (type === 'procedural') {
    return '程序式'
  }
  return '声明式'
}

function inertAssetCount(skill: Skill): number {
  return (skill.spec.assets ?? []).filter((a) => a.inert).length
}

function assetCount(skill: Skill): number {
  return (skill.spec.assets ?? []).length
}

async function loadSkills(): Promise<void> {
  loading.value = true
  try {
    skills.value = await listSkills()
  } catch (error) {
    ElMessage.error(describeSkillError('加载 Skill', error))
  } finally {
    loading.value = false
  }
}

/** 导入声明式 Skill（structured YAML）：整批先校验后落库。 */
async function handleImport(): Promise<void> {
  const yaml = yamlText.value.trim()
  if (!yaml) {
    ElMessage.warning('请先粘贴 Skill YAML')
    return
  }
  importing.value = true
  try {
    const imported = await importSkills(yaml)
    yamlText.value = ''
    ElMessage.success(`已导入 ${imported.length} 个 Skill`)
    await loadSkills()
  } catch (error) {
    ElMessage.error(describeSkillError('导入 Skill', error))
  } finally {
    importing.value = false
  }
}

/** 导入标准 SKILL.md 文本（procedural）：正文只作文本方法论参考，永不执行。 */
async function handleImportProceduralDocs(): Promise<void> {
  const md = mdText.value.trim()
  if (!md) {
    ElMessage.warning('请先粘贴 SKILL.md 文本')
    return
  }
  importing.value = true
  try {
    const imported = await importProceduralDocs(md)
    mdText.value = ''
    ElMessage.success(`已导入 ${imported.length} 个程序式 Skill`)
    await loadSkills()
  } catch (error) {
    ElMessage.error(describeSkillError('导入 SKILL.md', error))
  } finally {
    importing.value = false
  }
}

function onZipPicked(event: Event): void {
  const input = event.target as HTMLInputElement
  const file = input.files?.[0] ?? null
  zipFile.value = file
  zipPicked.value = file !== null
}

/** 上传技能包 zip（procedural 包）：仅根目录 SKILL.md 主文 + 白名单文本资产。 */
async function handleImportZip(): Promise<void> {
  if (!zipFile.value) {
    ElMessage.warning('请先选择技能包 zip 文件')
    return
  }
  importing.value = true
  try {
    const imported = await importProceduralZip(zipFile.value)
    zipFile.value = null
    zipPicked.value = false
    ElMessage.success(`已导入 ${imported.length} 个程序式 Skill 包`)
    await loadSkills()
  } catch (error) {
    ElMessage.error(describeSkillError('导入技能包', error))
  } finally {
    importing.value = false
  }
}

function resetZipInput(): void {
  zipFile.value = null
  zipPicked.value = false
  if (zipInputRef.value) {
    zipInputRef.value.value = ''
  }
}

/** 启停 Skill：成功后用后端返回记录同步本地状态，失败则回滚本地开关。 */
async function handleToggle(skill: Skill, enabled: boolean): Promise<void> {
  if (!canManage.value) {
    return
  }
  const previous = skill.enabled
  skill.enabled = enabled
  try {
    const updated = await setSkillEnabled(skill.name, enabled)
    Object.assign(skill, updated)
    ElMessage.success(enabled ? `已启用 ${skill.name}` : `已停用 ${skill.name}`)
  } catch (error) {
    skill.enabled = previous
    ElMessage.error(describeSkillError('切换 Skill 状态', error))
  }
}

onMounted(loadSkills)
</script>

<template>
  <section class="skill-page">
    <div class="page-head">
      <h2 class="title">
        Skill
        <el-icon class="title-icon"><MagicStick /></el-icon>
      </h2>
      <span class="desc">内置与自定义 Skill 的导入、启停与命中情况。</span>
      <el-button text class="refresh" :icon="Refresh" :loading="loading" @click="loadSkills">
        刷新
      </el-button>
    </div>

    <div class="stat-grid">
      <div class="card stat">
        <div class="l">Skill 总数</div>
        <div class="v mono">{{ totalCount }}</div>
      </div>
      <div class="card stat">
        <div class="l">已启用</div>
        <div class="v mono ok">{{ enabledCount }}</div>
      </div>
      <div class="card stat">
        <div class="l">已停用</div>
        <div class="v mono mute">{{ disabledCount }}</div>
      </div>
      <div class="card stat">
        <div class="l">累计命中</div>
        <div class="v mono">{{ totalHits }}</div>
      </div>
    </div>

    <div class="two-col">
      <div class="card list-card">
        <div class="row list-head">
          <span class="sub strong">Skill 清单</span>
          <span class="pill mute">{{ skills.length }} 个</span>
          <span class="faint grow">声明式方法论，不含任何执行动作字段</span>
        </div>
        <div v-if="loading" class="muted center-hint">加载中…</div>
        <div v-else-if="skills.length === 0" class="muted center-hint">
          暂无 Skill，可导入自定义 Skill YAML。
        </div>
        <div v-else class="skill-list">
          <div v-for="skill in skills" :key="skill.name" class="list-item">
            <div class="grow">
              <div class="row top">
                <span class="t">{{ skill.name }}</span>
                <span class="pill" :class="typeClass(skill.spec.type)">
                  {{ typeText(skill.spec.type) }}
                </span>
                <span class="pill mono">{{ skill.spec.version }}</span>
                <span class="pill" :class="sourceType(skill.source)">{{ sourceText(skill.source) }}</span>
              </div>
              <div v-if="skill.spec.description" class="muted desc ellipsis">
                {{ skill.spec.description }}
              </div>
              <div class="muted meta">
                <template v-if="skill.spec.type === 'procedural'">
                  <span>正文 {{ (skill.spec.body || '').length }} 字符</span>
                  <template v-if="assetCount(skill) > 0">
                    <span class="faint">·</span>
                    <span>{{ assetCount(skill) }} 个资产</span>
                  </template>
                  <template v-if="inertAssetCount(skill) > 0">
                    <span class="faint">·</span>
                    <span class="warn-tag">含 {{ inertAssetCount(skill) }} 个惰性脚本</span>
                  </template>
                </template>
                <template v-else>
                  <span>{{ skill.spec.domain }}</span>
                  <span class="faint">·</span>
                  <span>{{ skill.spec.kinds.length }} 类资源</span>
                  <span class="faint">·</span>
                  <span>{{ skill.spec.validation?.length || 0 }} 条规则</span>
                </template>
                <span class="faint">·</span>
                <span class="mono">命中 {{ Number(skill.hitCount) || 0 }}</span>
                <span class="faint">·</span>
                <span class="mono time">{{ formatTime(skill.lastHit) }}</span>
              </div>
            </div>
            <el-switch
              v-if="canManage"
              :model-value="skill.enabled"
              :disabled="importing"
              :aria-label="`启停 ${skill.name}`"
              @change="(value: string | number | boolean) => handleToggle(skill, Boolean(value))"
            />
            <span v-else class="pill" :class="skill.enabled ? 'info' : 'mute'">
              {{ skill.enabled ? '已启用' : '已停用' }}
            </span>
          </div>
        </div>
      </div>

      <div class="card import-card">
        <div class="sub strong" style="margin-bottom: 10px">
          <el-icon class="import-icon"><UploadFilled /></el-icon>
          导入自定义 Skill
        </div>
        <el-tabs v-model="importMode" class="import-tabs">
          <el-tab-pane label="YAML" name="structured">
            <el-input
              v-model="yamlText"
              type="textarea"
              :rows="12"
              placeholder="粘贴声明式 Skill YAML（支持 --- 多文档）"
              spellcheck="false"
            />
            <div class="row actions">
              <el-button
                type="primary"
                :icon="MagicStick"
                :loading="importing"
                :disabled="!yamlText.trim()"
                @click="handleImport"
              >
                导入
              </el-button>
            </div>
          </el-tab-pane>
          <el-tab-pane label="SKILL.md" name="procedural">
            <el-input
              v-model="mdText"
              type="textarea"
              :rows="12"
              placeholder="粘贴标准 SKILL.md 文本（支持多篇，正文只作文本参考，从不执行）"
              spellcheck="false"
            />
            <div class="row actions">
              <el-button
                type="primary"
                :icon="Document"
                :loading="importing"
                :disabled="!mdText.trim()"
                @click="handleImportProceduralDocs"
              >
                导入
              </el-button>
            </div>
          </el-tab-pane>
          <el-tab-pane label="技能包" name="zip">
            <div class="zip-row">
              <input
                ref="zipInputRef"
                type="file"
                accept=".zip,application/zip,application/x-zip-compressed"
                class="file-input"
                @change="onZipPicked"
              />
              <span class="muted small">{{ zipFile?.name || '未选择 zip 文件' }}</span>
            </div>
            <div class="row actions">
              <el-button
                type="primary"
                :icon="Upload"
                :loading="importing"
                :disabled="!zipPicked"
                @click="handleImportZip"
              >
                上传导入
              </el-button>
              <el-button v-if="zipPicked" text @click="resetZipInput">清除</el-button>
            </div>
          </el-tab-pane>
        </el-tabs>
      </div>
    </div>
  </section>
</template>

<style scoped>
.skill-page {
  display: flex;
  flex-direction: column;
  gap: 14px;
}

.page-head {
  display: flex;
  align-items: center;
  gap: 12px;
  background: var(--app-surface);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius);
  padding: 14px 16px;
}

.page-head .title {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  margin: 0;
  font-size: 16px;
}

.title-icon {
  color: var(--app-accent-2);
}

.page-head .desc {
  font-size: 12.5px;
  color: var(--app-muted);
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.refresh {
  margin-left: auto;
}

.stat-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(160px, 1fr));
  gap: 12px;
}

.card {
  background: var(--app-surface);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius);
}

.stat {
  display: flex;
  flex-direction: column;
  gap: 6px;
  padding: 14px 16px;
  min-width: 0;
}

.stat .l {
  font-size: 12px;
  color: var(--app-muted);
}

.stat .v {
  font-size: 22px;
  font-weight: 650;
}

.stat .v.ok {
  color: var(--app-accent-2);
}

.stat .v.mute {
  color: var(--app-muted);
}

.two-col {
  display: grid;
  grid-template-columns: minmax(0, 1fr) minmax(320px, 420px);
  gap: 12px;
  align-items: start;
}

.row {
  display: flex;
  align-items: center;
  gap: 8px;
}

.list-card {
  padding: 14px 16px;
}

.import-card {
  display: flex;
  flex-direction: column;
  gap: 12px;
  padding: 14px 16px;
}

.list-head {
  margin-bottom: 10px;
}

.import-icon {
  color: var(--app-accent-2);
  vertical-align: -2px;
  margin-right: 6px;
}

.grow {
  min-width: 0;
  flex: 1;
}

.sub {
  font-size: 13px;
}

.strong {
  font-weight: 650;
}

.pill {
  display: inline-flex;
  align-items: center;
  flex: none;
  padding: 2px 8px;
  font-size: 11px;
  line-height: 1.4;
  border-radius: 999px;
  background: rgba(99, 102, 241, 0.12);
  color: var(--app-accent);
}

.pill.info {
  background: rgba(99, 102, 241, 0.12);
  color: var(--app-accent);
}

.pill.success {
  background: rgba(52, 211, 153, 0.12);
  color: #34d399;
}

.pill.warning {
  background: rgba(251, 191, 36, 0.12);
  color: #fbbf24;
}

.pill.danger {
  background: rgba(248, 113, 113, 0.12);
  color: #f87171;
}

.pill.mute {
  background: var(--app-surface-hover);
  color: var(--app-muted);
}

.skill-list {
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.list-item {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 12px;
  border: 1px solid var(--app-hairline);
  border-radius: 8px;
  background: var(--app-surface-2);
}

.top {
  margin-bottom: 4px;
}

.t {
  font-size: 14px;
  font-weight: 650;
}

.desc {
  font-size: 12px;
}

.meta {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  font-size: 11.5px;
  margin-top: 6px;
}

.mono {
  font-family: var(--app-mono, ui-monospace, SFMono-Regular, Menlo, monospace);
}

.mono.time {
  color: var(--app-muted);
}

.warn-tag {
  color: #fbbf24;
}

.faint {
  color: var(--app-faint, var(--app-muted));
  opacity: 0.8;
}

.muted {
  color: var(--app-muted);
}

.center-hint {
  padding: 24px 8px;
  text-align: center;
  font-size: 12.5px;
}

.actions {
  justify-content: flex-end;
  margin-top: 4px;
}

.import-tabs :deep(.el-tabs__header) {
  margin-bottom: 10px;
}

.zip-row {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 10px 12px;
  border: 1px dashed var(--app-hairline);
  border-radius: 8px;
  background: var(--app-surface-2);
}

.file-input {
  max-width: 220px;
  font-size: 12px;
}

.small {
  font-size: 12px;
}

.ellipsis {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

@media (max-width: 1000px) {
  .two-col {
    grid-template-columns: 1fr;
  }
}
</style>
