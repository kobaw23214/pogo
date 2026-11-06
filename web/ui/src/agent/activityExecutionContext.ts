import { inject, provide, type InjectionKey, type Ref } from "vue";
import type { ActivityExecutionSnapshot } from "./activityExecution";

const key: InjectionKey<Readonly<Ref<ActivityExecutionSnapshot>>> = Symbol("activity-execution-evidence");
export function provideActivityExecution(evidence: Readonly<Ref<ActivityExecutionSnapshot>>): void { provide(key, evidence); }
export function useActivityExecution(): Readonly<Ref<ActivityExecutionSnapshot>> | undefined { return inject(key, undefined); }
