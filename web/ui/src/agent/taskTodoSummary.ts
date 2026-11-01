import type { TodoItem } from "@/api/types";

/** Counts reported states; blocked/cancelled are never passed off as completed. */
export function taskTodoCounts(todos: readonly TodoItem[]) {
  return {
    total: todos.length,
    completed: todos.filter(item => item.status === "completed").length,
    blocked: todos.filter(item => item.status === "blocked").length,
    cancelled: todos.filter(item => item.status === "cancelled").length,
    pending: todos.filter(item => item.status === "pending" || item.status === "in_progress").length,
  };
}
