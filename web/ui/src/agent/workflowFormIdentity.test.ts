import { describe, expect, it, vi } from "vitest";
import { nextTick, reactive, ref } from "vue";
import { watchWorkflowFormIdentity } from "./workflowFormIdentity";

describe("workflow forms across polled run snapshots", () => {
  it("keeps participant selection and its mounted form until the drawer or Run changes", async () => {
    const props = reactive({ open: false, run: { id: "run_a", revision: 1 } });
    const principal = ref("");
    const node = ref("");
    const reload = vi.fn(() => { principal.value = ""; node.value = ""; });
    const stop = watchWorkflowFormIdentity([() => props.open, () => props.run.id], reload);
    try {
      props.open = true;
      await nextTick();
      expect(reload).toHaveBeenCalledTimes(1);
      principal.value = "bob";
      node.value = "review";
      for (let revision = 2; revision <= 4; revision++) {
        props.run = { id: "run_a", revision };
        await nextTick();
      }
      expect(principal.value).toBe("bob");
      expect(node.value).toBe("review");
      expect(reload).toHaveBeenCalledTimes(1);
      props.run = { id: "run_b", revision: 1 };
      await nextTick();
      expect(principal.value).toBe("");
      expect(node.value).toBe("");
      expect(reload).toHaveBeenCalledTimes(2);
      props.open = false;
      await nextTick();
      expect(reload).toHaveBeenCalledTimes(3);
    } finally {
      stop();
    }
  });

  it("keeps approval input and submission state through polling but clears on another node or Run", async () => {
    const props = reactive({ nodeId: "review", run: { id: "run_a", revision: 1 } });
    const values = ref<Record<string, string>>({});
    const busy = ref(false);
    const reset = vi.fn(() => { values.value = {}; busy.value = false; });
    const stop = watchWorkflowFormIdentity([() => props.nodeId, () => props.run.id], reset);
    try {
      values.value = { comment: "审核意见应保留" };
      busy.value = true;
      props.run = { id: "run_a", revision: 2 };
      await nextTick();
      expect(values.value.comment).toBe("审核意见应保留");
      expect(busy.value).toBe(true);
      expect(reset).not.toHaveBeenCalled();
      props.nodeId = "second_review";
      await nextTick();
      expect(values.value).toEqual({});
      expect(busy.value).toBe(false);
      values.value = { comment: "另一节点" };
      props.run = { id: "run_b", revision: 1 };
      await nextTick();
      expect(values.value).toEqual({});
      expect(reset).toHaveBeenCalledTimes(2);
    } finally {
      stop();
    }
  });
});
