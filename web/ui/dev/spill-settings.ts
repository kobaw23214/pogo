import { createApp, h } from "vue";
import type { Composer } from "vue-i18n";
import ElementPlus from "element-plus";
import "element-plus/dist/index.css";
import "element-plus/theme-chalk/dark/css-vars.css";
import "../src/styles.scss";
import SettingsDialog from "../src/components/SettingsDialog.vue";
import { i18n } from "../src/i18n";
import { applyTheme } from "../src/theme";

// Mount the real settings page against an isolated local Go server configured
// with synthetic spill files. Unlike a mocked view, directory and file reads
// exercise the actual admin API. No model, Docker or deployed data is used.
(i18n.global as unknown as Composer).locale.value = "zh-CN";
applyTheme(new URLSearchParams(location.search).get("theme") === "dark" ? "graphite" : "paper", false);
createApp({
  render: () => h(SettingsDialog, { modelValue: true, embedded: true, section: "general" }),
}).use(i18n).use(ElementPlus).mount("#app");
