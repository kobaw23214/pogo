<script setup lang="ts">
import { ref } from "vue";
import { useI18n } from "vue-i18n";
import Logo from "./Logo.vue";
import { api, setToken } from "@/api/client";

// The gate, sibling of the cannot-connect screen (Unreachable.vue): the app's
// chrome stays mounted behind a veil while this takes the viewport.
//
// Two keys on one door. The token field is always there — a fixed access
// token carries no expiry and may be a personal one (users.json "token"), so
// pasting it enters that account's space. When the deployment has accounts,
// the username/password form is a toggle away: submit calls the login API and
// the server sets an HttpOnly cookie, so success just reloads and boot runs
// authorized.
const props = defineProps<{ rejected?: boolean; password?: boolean; published?: boolean }>();
const { t } = useI18n();

const value = ref("");
const shake = ref(false);
// Token first — it always works (the deployment token at minimum); the
// password form is the toggle away when accounts exist.
const tab = ref<"token" | "password">("token");

const username = ref("");
const passphrase = ref("");
const message = ref("");
const busy = ref(false);

function nudge() {
  shake.value = true;
  window.setTimeout(() => (shake.value = false), 400);
}

function submit() {
  const trimmed = value.value.trim();
  if (!trimmed) {
    nudge();
    return;
  }
  setToken(trimmed);
  window.location.reload();
}

async function submitPassword() {
  if (busy.value) return;
  if (!username.value.trim() || !passphrase.value) {
    message.value = "";
    nudge();
    return;
  }
  busy.value = true;
  message.value = "";
  try {
    await api.login(username.value.trim(), passphrase.value);
    window.location.reload();
  } catch (err) {
    const status = (err as { status?: number }).status;
    if (status === 429) message.value = t("loginGate.locked");
    else if (status === 401) message.value = t("loginGate.rejected");
    else message.value = (err as Error).message;
    nudge();
    busy.value = false;
  }
}
</script>

<template>
  <div class="veil">
    <div class="card" :class="{ shake }">
      <div class="mark">
        <Logo :size="40" />
      </div>
      <h1>{{ t(published ? "settingsCoverage.publishedTitle" : "tokenGate.title") }}</h1>
      <p class="sub">{{ t(published ? "settingsCoverage.publishedSubtitle" : "tokenGate.subtitle") }}</p>

      <p v-if="rejected" class="rejected">{{ t(published ? "settingsCoverage.publishedRejected" : "tokenGate.rejected") }}</p>
      <p v-else-if="message" class="rejected">{{ message }}</p>

      <template v-if="tab === 'token'">
        <ul v-if="!published" class="causes">
          <i18n-t keypath="tokenGate.hintStartup" tag="li" scope="global">
            <template #url><code>?token=</code></template>
          </i18n-t>
          <i18n-t keypath="tokenGate.hintDocker" tag="li" scope="global">
            <template #url2><code>http://localhost:17779/?token=…</code></template>
          </i18n-t>
        </ul>

        <form class="entry" @submit.prevent="submit">
          <input
            v-model="value"
            type="password"
            :placeholder="t('tokenGate.placeholder')"
            autocomplete="off"
            autocapitalize="off"
            spellcheck="false"
          />
          <button class="primary" type="submit">{{ t("tokenGate.enter") }}</button>
        </form>
      </template>

      <form v-else class="entry column" @submit.prevent="submitPassword">
        <input
          v-model="username"
          type="text"
          :placeholder="t('loginGate.username')"
          autocomplete="username"
          autocapitalize="off"
          spellcheck="false"
        />
        <input
          v-model="passphrase"
          type="password"
          :placeholder="t('loginGate.password')"
          autocomplete="current-password"
        />
        <button class="primary" type="submit" :disabled="busy">
          {{ busy ? t("loginGate.busy") : t("loginGate.enter") }}
        </button>
      </form>

      <button v-if="password" class="swap" type="button" @click="tab = tab === 'token' ? 'password' : 'token'; message = ''">
        {{ tab === "token" ? t("tokenGate.usePassword") : t("tokenGate.useToken") }}
      </button>
    </div>
  </div>
</template>

<style scoped lang="scss">
.veil {
  position: fixed;
  inset: 0;
  z-index: 1000; /* below element-plus poppers, above everything of ours */
  display: grid;
  place-items: center;
  background: color-mix(in srgb, var(--el-bg-color) 92%, transparent);
  backdrop-filter: blur(8px);
}

.card {
  max-width: 460px;
  padding: 32px;
  text-align: center;
  animation: rise 0.25s ease-out;

  &.shake {
    animation: shake 0.4s ease-out;
  }
}

@keyframes rise {
  from {
    opacity: 0;
    transform: translateY(8px);
  }
}

/* Empty submit or bad credentials: a nudge, not a punishment. */
@keyframes shake {
  20%,
  60% {
    transform: translateX(-6px);
  }
  40%,
  80% {
    transform: translateX(6px);
  }
}

.mark {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 76px;
  height: 76px;
  border-radius: 50%;
  background: var(--el-fill-color);
  color: var(--el-text-color-secondary);
  margin-bottom: 20px;
}

h1 {
  margin: 0 0 8px;
  font-size: 20px;
  font-weight: 600;
  color: var(--el-text-color-primary);
}

.sub {
  margin: 0 0 20px;
  font-size: 13px;
  line-height: 1.6;
  color: var(--el-text-color-secondary);
}

/* The credential was refused or the IP is cooling down. */
.rejected {
  margin: 0 0 14px;
  font-size: 12.5px;
  line-height: 1.6;
  color: var(--el-color-danger);
}

/* Where the token lives, in the same checklist voice as Unreachable's causes. */
.causes {
  margin: 0 0 16px;
  padding: 14px 18px;
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 12px;
  background: var(--el-fill-color-lighter);
  list-style: none;
  text-align: left;

  li {
    position: relative;
    padding-left: 16px;
    font-size: 12.5px;
    line-height: 1.7;
    color: var(--el-text-color-regular);

    & + li {
      margin-top: 6px;
    }

    &::before {
      content: "";
      position: absolute;
      left: 2px;
      top: 0.75em;
      width: 5px;
      height: 5px;
      border-radius: 50%;
      background: var(--el-text-color-placeholder);
    }

    code {
      padding: 1px 5px;
      border-radius: 4px;
      background: var(--el-fill-color);
      border: 1px solid var(--el-border-color-lighter);
      font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
      font-size: 11px;
    }
  }
}

.entry {
  display: flex;
  gap: 10px;

  &.column {
    flex-direction: column;
  }

  input {
    flex: 1;
    min-width: 0;
    padding: 8px 12px;
    border: 1px solid var(--el-border-color);
    border-radius: 8px;
    background: var(--el-bg-color);
    color: var(--el-text-color-primary);
    font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
    font-size: 13px;

    &:focus {
      outline: none;
      border-color: var(--el-color-primary);
    }
  }

  .primary {
    padding: 8px 20px;
    border: 1px solid var(--el-color-primary);
    border-radius: 8px;
    background: var(--el-color-primary);
    color: #fff;
    font-size: 13px;
    cursor: pointer;

    &:hover {
      opacity: 0.88;
    }

    &:disabled {
      opacity: 0.6;
      cursor: default;
    }
  }
}

/* The one-click hop between the two keys; quiet on purpose. */
.swap {
  margin-top: 16px;
  padding: 0;
  border: none;
  background: none;
  color: var(--el-color-primary);
  font-size: 12.5px;
  cursor: pointer;

  &:hover {
    text-decoration: underline;
  }
}
</style>
