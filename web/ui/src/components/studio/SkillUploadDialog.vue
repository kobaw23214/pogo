<script setup lang="ts">
import { computed, onBeforeUnmount, ref, shallowRef } from 'vue';
import { Check, Upload } from '@element-plus/icons-vue';
import { api } from '@/api/client';
import type { SkillUploadResult } from '@/api/types';
import type { SkillExecutionStatus } from '@/api/skillTypes';
import { canConfigureUploadedSkill, skillUploadState } from '@/agent/skillExecution';
import { registerStudioNavigationGuard } from '@/agent/studioNavigation';
import ConfirmDialog from '@/components/ConfirmDialog.vue';
import SkillExecutionNotice from '@/components/SkillExecutionNotice.vue';
import { formatSkillBytes as bytes, skillFileError as fileError, skillError as errorText } from './skillManagement';

const props = defineProps<{ maxBytes: number; execution?: SkillExecutionStatus | null }>();
const emit = defineEmits<{ close: []; installed: [result: SkillUploadResult]; create: [ids: string[], name: string] }>();
const input = ref<HTMLInputElement | null>(null);
const file = ref<File | null>(null);
const phase = ref<'idle' | 'uploading' | 'installing' | 'confirm' | 'resolving' | 'done' | 'error' | 'cancelled'>('idle');
const error = ref('');
const loaded = ref(0);
const total = ref(0);
const speed = ref(0);
const dragDepth = ref(0);
const result = ref<SkillUploadResult | null>(null);
const executionPending = computed(() => result.value !== null && skillUploadState(result.value) === 'pending');
// The parked decision from a confirm_replace answer: the server keeps the
// staged bytes, the token spends exactly once.
const pending = ref<SkillUploadResult | null>(null);
const busy = computed(() => phase.value === 'uploading' || phase.value === 'installing' || phase.value === 'resolving');
const percent = computed(() => total.value ? Math.min(100, Math.floor(100 * loaded.value / total.value)) : 0);
const controller = shallowRef<AbortController | null>(null);
let disposed = false;

async function upload(candidate: File) {
  if (busy.value) return;
  file.value = candidate; result.value = null; pending.value = null;
  error.value = fileError(candidate, props.maxBytes);
  if (error.value) { phase.value = 'error'; return; }
  phase.value = 'uploading'; loaded.value = 0; total.value = candidate.size; speed.value = 0;
  const started = performance.now();
  const current = new AbortController(); controller.value = current;
  try {
    const installed = await api.uploadSkill(candidate, {
      signal: current.signal,
      onProgress(bytes, size) {
        if (disposed) return;
        loaded.value = bytes; total.value = size;
        const seconds = (performance.now() - started) / 1000;
        if (seconds > .5) speed.value = bytes / seconds;
      },
      onUploaded() { if (!disposed) { loaded.value = total.value; phase.value = 'installing'; } },
    });
    if (disposed) return;
    if (installed.status === 'confirm_replace' && installed.confirm_token && installed.existing?.length) {
      // Same package, other version: nothing is installed yet — the decision
      // below decides whether the staged bytes become the new version.
      pending.value = installed; phase.value = 'confirm';
      return;
    }
    finishInstall(installed);
  } catch (e) {
    if (disposed) return;
    const uncertain = (phase.value as typeof phase.value | 'installing') === 'installing';
    phase.value = e instanceof DOMException && e.name === 'AbortError' ? 'cancelled' : 'error';
    error.value = phase.value === 'cancelled' ? '上传已取消，可重新选择文件。' : errorText(e);
    if (uncertain && !(e as { status?: number }).status) error.value += ' 文件可能已安装，请刷新列表确认。';
  } finally { if (controller.value === current) controller.value = null; }
}

function finishInstall(installed: SkillUploadResult) {
  if (!installed.name && installed.status !== 'kept_old') throw new Error('服务器未返回安装结果，请刷新 Skill 列表确认。');
  result.value = installed; phase.value = 'done';
  if (installed.status !== 'kept_old') {
    emit('installed', installed);
    window.dispatchEvent(new CustomEvent('pogo-skills-changed'));
  }
}

async function resolve(action: 'replace' | 'keep-old') {
  const token = pending.value?.confirm_token;
  if (!token || busy.value) return;
  phase.value = 'resolving'; error.value = '';
  try {
    finishInstall(await api.resolveSkillUpload(token, action));
  } catch (e) {
    if (!disposed) { phase.value = 'error'; error.value = errorText(e); }
  }
}

function fmtDate(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  const p = (n: number) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`;
}
function pick() { if (!busy.value) input.value?.click(); }
function picked(event: Event) {
  const target = event.target as HTMLInputElement; const candidate = target.files?.[0]; target.value = '';
  if (candidate) void upload(candidate);
}
function drag(event: DragEvent, delta: number) {
  if (!event.dataTransfer?.types.includes('Files')) return;
  event.preventDefault(); if (!busy.value) dragDepth.value = Math.max(0, dragDepth.value + delta);
}
function dropped(event: DragEvent) {
  event.preventDefault(); dragDepth.value = 0;
  if (busy.value) return;
  if (event.dataTransfer?.files.length !== 1) { error.value = '一次上传一个 Skill ZIP。'; return; }
  void upload(event.dataTransfer.files[0]!);
}
const leaveConfirm = ref<InstanceType<typeof ConfirmDialog> | null>(null);
function confirmInterrupt(): boolean | Promise<boolean> {
  if (!busy.value) return true;
  // installing/resolving are already server-side; leaving cannot undo them.
  const serverSide = phase.value === 'installing' || phase.value === 'resolving';
  return leaveConfirm.value?.ask({
    title: '中断上传？',
    body: serverSide ? '服务端仍在安装，离开后请刷新 Skill 列表确认结果。' : '离开将中断本次上传，已传输的进度不会保留。',
    confirmText: '中断并离开',
    cancelText: '继续等待',
    danger: true,
  }) ?? false;
}
async function close() { if (await confirmInterrupt()) emit('close'); }
const unregister = registerStudioNavigationGuard(async () => {
  if (!(await confirmInterrupt())) return false;
  controller.value?.abort();
  emit('close');
  return true;
});
function beforeUnload(event: BeforeUnloadEvent) { if (busy.value) { event.preventDefault(); event.returnValue = ''; } }
window.addEventListener('beforeunload', beforeUnload);
onBeforeUnmount(() => { disposed = true; controller.value?.abort(); unregister(); window.removeEventListener('beforeunload', beforeUnload); });
</script>

<template>
  <el-dialog :model-value="true" title="上传 Skill" width="min(660px, 94vw)" append-to-body :close-on-click-modal="false" :before-close="close">
    <div class="upload-intro">上传 Skill 的说明和所需文件，再为 Agent 配置。工具能否执行还需检查运行环境与依赖。</div>
    <SkillExecutionNotice v-if="!result" :execution="execution" />
    <input ref="input" type="file" accept=".zip,application/zip" hidden @change="picked" />
    <div class="dropzone" :class="{ over: dragDepth > 0, busy, complete: phase === 'done' && !executionPending }" @dragenter="drag($event, 1)" @dragleave="drag($event, -1)" @dragover.prevent @drop="dropped">
      <span class="upload-icon"><el-icon><Check v-if="phase === 'done'" /><Upload v-else /></el-icon></span>
      <strong>{{ file?.name ?? '拖入 Skill ZIP，或选择文件' }}</strong>
      <span class="hint">{{ file ? bytes(file.size) : maxBytes > 0 ? `最大 ${bytes(maxBytes)}` : '支持包含工具、运行环境和数据的大文件包' }}</span>
      <button v-if="!busy" class="button" type="button" @click="pick">{{ file ? '重新选择文件' : '选择 ZIP 文件' }}</button>
      <div v-if="busy" class="transfer" role="status" aria-live="polite">
        <div class="transfer-label"><span>{{ phase === 'installing' ? '传输完成，正在校验并安装…' : phase === 'resolving' ? '正在安装新版本并清理旧版本…' : `正在上传 ${percent}%` }}</span><span>{{ phase === 'uploading' ? `${bytes(loaded)} / ${bytes(total)}` : '' }}</span></div>
        <div class="progress" role="progressbar" aria-label="上传进度" :aria-valuenow="percent" aria-valuemin="0" aria-valuemax="100"><span :style="{ width: `${percent}%` }" /></div>
        <div class="transfer-label"><span>{{ phase === 'uploading' ? `${bytes(speed)}/s` : '大型工具包解包需要一些时间，请保持页面打开。' }}</span><button v-if="phase === 'uploading'" type="button" class="text-button" @click="controller?.abort()">取消上传</button></div>
      </div>
    </div>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
    <div v-if="phase === 'confirm' && pending" class="confirm-box" role="alertdialog" aria-label="确认替换 Skill 版本">
      <strong>检测到同一 Skill 的其他版本</strong>
      <p>本次上传「{{ pending.incoming?.name || pending.incoming?.id }}」v{{ pending.incoming?.version }}，已安装版本：</p>
      <ul><li v-for="v in pending.existing" :key="v.skill_id">v{{ v.version }}<span v-if="v.installed_at"> · 安装于 {{ fmtDate(v.installed_at) }}</span><span v-if="v.revoked"> · 已停用</span></li></ul>
      <p class="hint">替换：安装新版本并删除上面列出的旧版本；仍引用旧版本的会话会保留，但不再能使用该 Skill。使用旧版本：放弃本次上传，已安装的内容保持不变。</p>
    </div>
    <div v-if="result" class="installed" :class="{ pending: executionPending }" role="status">
      <template v-if="result.status === 'kept_old'">
        <strong>已保留旧版本，本次上传未安装</strong><p>暂存在服务器上的上传文件已丢弃，Skill 列表保持不变。</p>
      </template>
      <template v-else>
        <strong>{{ result.name }} 已安装<span v-if="result.version"> · v{{ result.version }}</span><span v-if="executionPending"> · 执行未就绪</span></strong>
        <p v-if="executionPending">可先配置 Agent 并保存草稿；发布前需完成运行环境与依赖检查。</p>
        <p v-else-if="result.ready === true">工具与依赖已就绪，可为 Agent 配置使用。</p>
        <p v-else>Skill 内容已安装，可在创建或编辑 Agent 时选择。</p>
        <SkillExecutionNotice :execution="result.execution" :issues="result.issues" />
      </template>
      <ul v-if="result.warnings?.length"><li v-for="warning in result.warnings" :key="warning">{{ warning }}</li></ul>
    </div>
    <details class="format-help"><summary>如何组织 Skill 包</summary><p>普通 Skill 包含 <code>SKILL.md</code> 和可选的参考资料、脚本。需要携带独立工具环境时，增加 <code>pogo-skill.json</code> 描述工具、运行时和数据；系统自动完成安装。均可包在单个文件夹内，文件夹名须与 SKILL.md 的 name 一致。</p><p>仅接受标准 Skill ZIP，根目录必须包含带有 name 和 description 的 SKILL.md。同一版本的工具包不能被不同内容覆盖，请更新版本号。</p></details>
    <template #footer>
      <div class="dialog-actions">
        <template v-if="phase === 'confirm'">
          <button class="button" type="button" :disabled="busy" @click="resolve('keep-old')">使用旧版本</button>
          <button class="button primary" type="button" :disabled="busy" @click="resolve('replace')">替换为新版本</button>
        </template>
        <template v-else>
          <button class="button" type="button" @click="close">{{ result ? '完成' : '关闭' }}</button>
          <button v-if="result && canConfigureUploadedSkill(result)" class="button primary" type="button" @click="emit('create', result.skill_ids!, result.name)">{{ executionPending ? '用此 Skill 创建草稿' : '用此 Skill 创建 Agent' }}</button>
        </template>
      </div>
    </template>
  </el-dialog>
  <ConfirmDialog ref="leaveConfirm" />
</template>

<style scoped>
.upload-intro,.hint,.format-help{color:var(--el-text-color-secondary);line-height:1.8}.upload-intro{margin:0 0 20px}.dropzone{display:flex;flex-direction:column;align-items:center;gap:12px;padding:34px 24px;border:1px dashed var(--el-border-color);border-radius:12px;background:var(--el-fill-color-lighter);text-align:center}.dropzone.over{border-color:var(--el-color-primary);background:var(--el-color-primary-light-9)}.dropzone.complete{border-color:var(--el-color-success)}.dropzone strong{overflow-wrap:anywhere;font-size:15px;font-weight:550}.upload-icon{width:46px;height:46px;display:grid;place-items:center;border:1px solid var(--el-border-color);border-radius:12px;font-size:24px;color:var(--el-color-primary)}.button{padding:9px 15px;border:1px solid var(--el-border-color);border-radius:6px;background:var(--el-bg-color);color:var(--el-text-color-primary);font:inherit;cursor:pointer}.primary{margin-left:10px;color:var(--el-color-white);background:var(--el-color-primary);border-color:transparent}.transfer{width:100%;font-size:12px}.transfer-label{display:flex;align-items:center;justify-content:space-between;gap:12px;margin:10px 0;color:var(--el-text-color-secondary)}.progress{height:5px;border-radius:5px;background:var(--el-fill-color-darker);overflow:hidden}.progress>span{display:block;height:100%;background:var(--el-color-primary);transition:width .15s}.text-button{border:0;background:none;font:inherit;color:var(--el-color-primary);cursor:pointer}.error{color:var(--el-color-danger);line-height:1.7;overflow-wrap:anywhere}.confirm-box{margin-top:16px;padding:16px;background:var(--el-color-warning-light-9);border-radius:8px;line-height:1.8}.confirm-box strong{display:block}.confirm-box ul{margin:8px 0;padding-left:20px}.installed{margin-top:16px;padding:16px;background:var(--el-color-success-light-9);border-radius:8px;line-height:1.7}.installed p{margin:6px 0 0}.format-help{margin-top:20px;font-size:12px}.format-help summary{cursor:pointer}.format-help code{color:var(--el-text-color-regular)}
/* The dialog teleports to body, so footer layout must live on slot content
 * (which keeps this component's scope attribute), not on :deep selectors. */
.dialog-actions{display:flex;justify-content:flex-end;align-items:center;gap:10px;flex-wrap:wrap}
.dialog-actions .button{flex:0 0 auto}
.installed.pending{background:var(--el-color-warning-light-9)}
</style>
