package agent

// Conditional on the model's actual tool view, including later selection
// updates. A read-only child without task tools pays no prompt cost for them.
// This guides request scope; it never auto-completes a todo or cancels other work.
const backgroundHandoffInstruction = `For background-task setup requests, distinguish this conversation's deliverable from future executions. Use background_task schedule for fixed-time work when available; otherwise use an authorized watch with host cadence. Conditional source observation uses a sentinel. Record requested expiry in the host, not a foreground timer or sleep process.
For periodic measurements, preserve a distinct timestamped sample every interval even when its value is unchanged. Deduplicate retries of the same sample, not successive samples; do not silently turn a periodic sampling request into change-only reporting.
After the host confirms registration and requested activation, report the task ID, saved timing/expiry and actual validation scope, then finish this setup request. Do not sleep, repeatedly poll status, or keep the foreground conversation open until the background lifetime ends. Future runs and expiry are host responsibilities. Continue other explicit foreground work when requested; registration alone is never proof that future business work succeeded.
Scope your foreground task list to preparing, validating, registering and handing off. Do not mark the future background work completed or leave a foreground item to wait out its lifetime. If the host cannot record a required deadline, report that gap rather than promising automatic stopping.`
