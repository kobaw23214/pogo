<script setup lang="ts">
import { useI18n } from "vue-i18n";
const { t: tr } = useI18n();
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
    validationError.value = tr('studioManagement.text477');
    return;
  }
  if (authMode.value === "token" && !accessKey.value.trim()) {
    validationError.value = tr('studioManagement.text478');
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
      <button class="dialog-close" type="button" :aria-label="tr('studioManagement.text479')" :disabled="busy" @click="requestClose">×</button>
      <div class="login-art" aria-hidden="true">
        <div class="login-paper art-shell"></div><div class="login-paper-front art-wave"></div>
        <div class="login-art-title" style="white-space:pre-line">{{ tr('studioLandingLabels.nextPossibility') }}<span>{{ tr('studioManagement.text480') }}</span></div>
        <span class="login-art-meta">POGO / {{ tr('studioLandingLabels.foundation') }}</span>
      </div>
      <form class="login-form" :aria-busy="busy" @submit.prevent="onSubmit">
        <Logo :size="40" class="login-logo" />
        <p class="eyebrow">{{ tr('studioLandingLabels.welcome') }}</p>
        <h2 id="studio-login-title">{{ tr('studioManagement.text481') }}<br>{{ tr('studioManagement.text482') }}</h2>
        <div v-if="passwordEnabled" class="auth-switch" role="group" :aria-label="tr('studioManagement.text483')">
          <button type="button" :aria-pressed="authMode === 'password'" :disabled="busy" @click="switchMode('password')">{{ tr('studioManagement.text484') }}</button>
          <button type="button" :aria-pressed="authMode === 'token'" :disabled="busy" @click="switchMode('token')">{{ tr('studioManagement.text485') }}</button>
        </div>
        <p v-else class="login-help">{{ tr('studioManagement.text486') }}</p>
        <template v-if="authMode === 'password'">
          <label for="studio-login-username">{{ tr('studioManagement.text487') }}</label>
          <input id="studio-login-username" ref="usernameInput" v-model="username" type="text" :placeholder="tr('studioManagement.text488')" autocomplete="username" autocapitalize="off" :spellcheck="false" :disabled="busy" required @input="clearError" />
          <label for="studio-login-password">{{ tr('studioManagement.text489') }}</label>
          <div class="secret-field">
            <input id="studio-login-password" v-model="password" :type="showSecret ? 'text' : 'password'" :placeholder="tr('studioManagement.text490')" autocomplete="current-password" :disabled="busy" required @input="clearError" />
            <button type="button" class="secret-toggle" :aria-label="showSecret ? tr('studioManagement.text491') : tr('studioManagement.text492')" :aria-pressed="showSecret" :disabled="busy" @click="showSecret = !showSecret">{{ showSecret ? tr('studioManagement.text493') : tr('studioManagement.text494') }}</button>
          </div>
        </template>
        <template v-else>
          <label for="studio-login-token">{{ tr('studioManagement.text485') }}</label>
          <div class="secret-field">
            <input id="studio-login-token" ref="tokenInput" v-model="accessKey" :type="showSecret ? 'text' : 'password'" :placeholder="tr('studioManagement.text495')" autocomplete="off" autocapitalize="off" :spellcheck="false" :disabled="busy" required @input="clearError" />
            <button type="button" class="secret-toggle" :aria-label="showSecret ? tr('studioManagement.text496') : tr('studioManagement.text497')" :aria-pressed="showSecret" :disabled="busy" @click="showSecret = !showSecret">{{ showSecret ? tr('studioManagement.text493') : tr('studioManagement.text494') }}</button>
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
        <button class="button button-dark login-submit" type="submit" :disabled="busy"><span>{{ busy ? tr('studioManagement.text498') : tr('studioManagement.text372') }}</span><span class="login-arrow" :class="{ 'login-arrow-hidden': busy }" aria-hidden="true">↗</span></button>
        <p class="login-footnote">{{ tr('studioManagement.text499') }}</p>
      </form>
    </dialog>
  </div>
</template>

<style scoped src="./studioLogin.css"></style>
