<script setup lang="ts">
import { useI18n } from "vue-i18n";
import ThemePicker from "@/components/ThemePicker.vue";
import { LOCALE_LABELS, SUPPORTED_LOCALES, setLocale, type Locale } from "@/i18n";
const { locale } = useI18n();
</script>

<template>
  <section class="preferences" aria-label="外观与语言">
    <div class="preferences-copy"><h2>外观与语言</h2><p>Studio 与对话工作台共用同一套外观。</p></div>
    <div class="preference-control"><span>主题</span><ThemePicker placement="bottom-start" /></div>
    <div class="preference-control">
      <label for="studio-language">语言</label>
      <select id="studio-language" :value="locale" @change="setLocale(($event.target as HTMLSelectElement).value as Locale)">
        <option v-for="language in SUPPORTED_LOCALES" :key="language" :value="language">{{ LOCALE_LABELS[language] }}</option>
      </select>
    </div>
  </section>
</template>

<style scoped>
.preferences{display:flex;align-items:center;flex-wrap:wrap;gap:20px 32px;padding:22px 24px;border-bottom:1px solid var(--el-border-color-lighter);background:var(--el-bg-color)}
.preferences-copy{flex:1 1 240px}.preferences-copy h2{font-size:14px;font-weight:600;margin:0 0 6px}.preferences-copy p{margin:0;color:var(--el-text-color-secondary);font-size:12px}
.preference-control{display:flex;align-items:center;gap:12px;font-size:12px;color:var(--el-text-color-regular)}
select{font:inherit;color:var(--el-text-color-primary);border:1px solid var(--el-border-color);border-radius:7px;background:var(--el-bg-color);padding:7px 10px;min-width:96px}
select:focus-visible{outline:2px solid var(--el-color-primary);outline-offset:3px}
@media(max-width:720px){.preferences{padding:18px 14px;gap:14px 24px}.preferences-copy{flex-basis:100%}}
</style>
