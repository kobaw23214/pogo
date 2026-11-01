<script setup lang="ts">
import { nextTick, onBeforeUnmount, ref, watch } from "vue";
import Logo from "@/components/Logo.vue";
import StudioLanding from "./StudioLanding.vue";

// Presentation only: StudioShell remains the owner of both authentication paths.
const props = defineProps<{ passwordEnabled: boolean; busy: boolean; error: string }>();
const emit = defineEmits<{
  submit: [payload: { mode: "password" | "token"; username: string; password: string; accessKey: string }];
  "clear-error": [];
}>();
const dialog = ref<HTMLDialogElement | null>(null);
const usernameInput = ref<HTMLInputElement | null>(null);
const tokenInput = ref<HTMLInputElement | null>(null);
const errorMessage = ref<HTMLElement | null>(null);
const loginOpen = ref(false);
const authMode = ref<"password" | "token">("password");
const username = ref("");
const password = ref("");
const accessKey = ref("");
const showSecret = ref(false);
const validationError = ref("");
let opener: HTMLElement | null = null;
let disposed = false;

function focusEntry() {
  if (authMode.value === "password") usernameInput.value?.focus();
  else tokenInput.value?.focus();
}

async function openLogin(trigger?: HTMLElement) {
  if (trigger) opener = trigger;
  if (disposed) return;
  loginOpen.value = true;
  await nextTick();
  if (disposed || !dialog.value) return;
  if (!dialog.value.open) dialog.value.showModal();
  focusEntry();
}

function requestClose() {
  if (!props.busy) dialog.value?.close();
}

function onCancel(event: Event) {
  if (props.busy) event.preventDefault();
}

function onClose() {
  if (disposed) return;
  loginOpen.value = false;
  showSecret.value = false;
  password.value = "";
  accessKey.value = "";
  validationError.value = "";
  emit("clear-error");
  if (opener?.isConnected) opener.focus({ preventScroll: true });
}

function onBackdrop(event: MouseEvent) {
  if (event.target !== dialog.value || props.busy) return;
  const rect = dialog.value?.getBoundingClientRect();
  if (rect && (event.clientX < rect.left || event.clientX > rect.right || event.clientY < rect.top || event.clientY > rect.bottom)) requestClose();
}

function clearError() {
  validationError.value = "";
  emit("clear-error");
}

function switchMode(mode: "password" | "token") {
  if (props.busy || (mode === "password" && !props.passwordEnabled)) return;
  authMode.value = mode;
  showSecret.value = false;
  clearError();
  void nextTick(focusEntry);
}

function onSubmit() {
  if (props.busy) return;
  clearError();
  if (authMode.value === "password" && (!username.value.trim() || !password.value)) {
    validationError.value = "请输入账号和密码";
    return;
  }
  if (authMode.value === "token" && !accessKey.value.trim()) {
    validationError.value = "请输入工作空间访问密钥";
    return;
  }
  emit("submit", { mode: authMode.value, username: username.value, password: password.value, accessKey: accessKey.value });
}

watch(() => props.passwordEnabled, (enabled) => {
  authMode.value = enabled ? "password" : "token";
  showSecret.value = false;
}, { immediate: true });

watch(() => props.error, async (message) => {
  if (!message) return;
  if (!loginOpen.value) await openLogin();
  await nextTick();
  if (!disposed) errorMessage.value?.focus();
});

onBeforeUnmount(() => {
  disposed = true;
  dialog.value?.close();
});
</script>

<template>
  <div class="studio-login">
    <StudioLanding :login-open="loginOpen" @login="openLogin" />
    <dialog ref="dialog" class="login-dialog" aria-labelledby="studio-login-title" @cancel="onCancel" @close="onClose" @click="onBackdrop">
      <button class="dialog-close" type="button" aria-label="关闭登录面板" :disabled="busy" @click="requestClose">×</button>
      <div class="login-art" aria-hidden="true">
        <div class="login-paper art-shell"></div><div class="login-paper-front art-wave"></div>
        <div class="login-art-title">Your next<br>possibility.<span>从这里，展开。</span></div>
        <span class="login-art-meta">POGO / AGENT FOUNDATION</span>
      </div>
      <form class="login-form" :aria-busy="busy" @submit.prevent="onSubmit">
        <Logo :size="40" class="login-logo" />
        <p class="eyebrow">WELCOME TO POGO</p>
        <h2 id="studio-login-title">回到你的<br>工作空间。</h2>
        <div v-if="passwordEnabled" class="auth-switch" role="group" aria-label="登录方式">
          <button type="button" :aria-pressed="authMode === 'password'" :disabled="busy" @click="switchMode('password')">账号密码</button>
          <button type="button" :aria-pressed="authMode === 'token'" :disabled="busy" @click="switchMode('token')">访问密钥</button>
        </div>
        <p v-else class="login-help">使用工作空间访问密钥继续。</p>
        <template v-if="authMode === 'password'">
          <label for="studio-login-username">账号</label>
          <input id="studio-login-username" ref="usernameInput" v-model="username" type="text" placeholder="工作空间账号" autocomplete="username" autocapitalize="off" :spellcheck="false" :disabled="busy" required @input="clearError" />
          <label for="studio-login-password">密码</label>
          <div class="secret-field">
            <input id="studio-login-password" v-model="password" :type="showSecret ? 'text' : 'password'" placeholder="输入密码" autocomplete="current-password" :disabled="busy" required @input="clearError" />
            <button type="button" class="secret-toggle" :aria-label="showSecret ? '隐藏密码' : '显示密码'" :aria-pressed="showSecret" :disabled="busy" @click="showSecret = !showSecret">{{ showSecret ? '隐藏' : '显示' }}</button>
          </div>
        </template>
        <template v-else>
          <label for="studio-login-token">访问密钥</label>
          <div class="secret-field">
            <input id="studio-login-token" ref="tokenInput" v-model="accessKey" :type="showSecret ? 'text' : 'password'" placeholder="输入工作空间访问密钥" autocomplete="off" autocapitalize="off" :spellcheck="false" :disabled="busy" required @input="clearError" />
            <button type="button" class="secret-toggle" :aria-label="showSecret ? '隐藏密钥' : '显示密钥'" :aria-pressed="showSecret" :disabled="busy" @click="showSecret = !showSecret">{{ showSecret ? '隐藏' : '显示' }}</button>
          </div>
        </template>
        <!-- The error row is always mounted with a reserved one-line height:
             inserting it on failure used to grow the form and re-center the
             whole dialog — the "card stutters on click" this exists to kill.
             Empty state is visibility, not v-if, so no click ever shifts
             layout; a rare multi-line message may still grow the slot, from
             a stable base. -->
        <p
          ref="errorMessage"
          class="form-error"
          :class="{ 'form-error-empty': !error && !validationError }"
          role="alert"
          :aria-hidden="!(error || validationError)"
          tabindex="-1"
        >{{ error || validationError || '\u00A0' }}</p>
        <button class="button button-dark login-submit" type="submit" :disabled="busy"><span>{{ busy ? '正在登录…' : '进入工作空间' }}</span><span class="login-arrow" :class="{ 'login-arrow-hidden': busy }" aria-hidden="true">↗</span></button>
        <p class="login-footnote">你的工作空间，你的模型、技能与工具。</p>
      </form>
    </dialog>
  </div>
</template>

<style scoped src="./studioLogin.css"></style>
