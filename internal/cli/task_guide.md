# Posse tasks: coordinate other agents

`posse task` dispatches work to other agents (claude, codex, grok or cursor), each in its own Posse terminal the user can watch, and brings their results back to you. Workers report through Posse; never scrape their terminals for results.

**Done means:** every task you started has one explicit outcome, and your report to the user names, per task, the outcome, the evidence and anything unresolved.

## Pick your role

- The user wants work supervised, parallelized or fanned out, with results coming back to you: you are the coordinator; use `posse task` as below.
- The user only wants another agent to take something over: start it with `posse terminal create <workspace> --agent <name> --prompt "..."` or `posse workspace create <project> <name> --agent <name> --prompt "..."` (both also take `--model` and `--effort`, as below). Create no task and do not wait for it.
- `POSSE_TASK_ID` is set: you are a worker. Follow the instructions you were started with; you may dispatch sub-workers only for work inside your own task.

## Dispatch

```sh
posse task start --agent codex [--model <model>] [--effort <level>] --title "short title" [--worktree <name>] --spec-file - <<'SPEC'
<self-contained task spec>
SPEC
```

A spec must stand on its own; the worker cannot see your conversation. Name:

- **Target:** the files, component, device or environment in scope.
- **Result:** the concrete change or answer to produce.
- **Constraints:** what must stay true and what not to touch.
- **Ownership:** which files the worker may edit.
- **Acceptance:** the test, output or evidence that proves it is done.

`--model` and `--effort` pick the worker's model and reasoning effort; leave them out and the agent uses its own defaults. Set them when the user names a model or effort, and otherwise only when the work clearly needs a stronger or a cheaper setting. Values go to the agent unchanged, so use its own names:

- claude: `opus`, `sonnet`, `haiku` or a full model ID; effort `low`, `medium`, `high`, `xhigh` or `max`.
- codex: an OpenAI model ID such as `gpt-5.5`; effort as in codex's `model_reasoning_effort`, e.g. `low`, `medium`, `high`, `xhigh`.
- grok: a model from `grok models`; effort as in grok's `--reasoning-effort`.
- cursor: a model from `cursor-agent models`; `--effort` needs `--model` and becomes `<model>[effort=<level>]`.

A wrong name usually makes the worker fail at startup; `posse task show <task>` then shows the agent's error.

Start every independent task before you end your turn. Work with an order runs in waves: start the next wave when the reminder tells you the one it depends on has settled. Parallel workers that edit code each get `--worktree <name>` (git projects only), so their edits never collide. Workers nest at most 3 deep; at the limit, do the work yourself. `--workspace <name>` starts a worker in another workspace.

## Wait and answer

Do not block on workers by default. After you start the tasks, tell the user what is running and end your turn, or keep doing your own part of the work. The user can keep talking to you meanwhile. When a worker reports or asks, Posse gives you a reminder line ("Posse: k_… finished. Run `posse task inbox` to read."): next to a tool result while you work, when your turn ends, or typed into your terminal while you are idle. Then read and handle the messages:

```sh
posse task inbox    # unread messages from your workers; never blocks
```

This holds even when the user asks to hear the result ("…and tell me how it went"): say what is running, end your turn, and report when the reminder arrives. While you block, the user cannot talk to you.

Block only when the user explicitly asks you to wait for the workers, or when you are not running as an interactive agent (a script):

```sh
posse task wait     # blocks until a worker finishes, asks or exits; up to 10 minutes, so give your shell tool a 10-minute timeout
```

Each message is one of:

- `done` / `failed`: the worker's summary and report file. Check what matters before you rely on it.
- `question`: the worker is blocked until you run `posse task reply <message-id> "<answer>"`. Answer it as soon as you see it; decide yourself when you can and ask the user only when you cannot.
- `exited`: the worker stopped without reporting. `posse task show <task>` includes the end of its screen.

A wait that times out or returns nothing is a checkpoint, not a failure. After three empty waits in a row, look with `posse task list` and `posse task show <task>`. Only a worker that has exited (`exited` or `lost`) or reported `failed` may be retried or given up on; silence alone is not proof.

`posse task send <task> "<message>"` reaches the worker at its next checkpoint; sending does not mean it has read it. It also works after the worker reported `done` or `failed`: the task goes back to running and the same worker, with everything it already knows, does the follow-up and reports again. Prefer this over starting a new task for follow-ups, fixes after review or related work in the same area.

## Accept, retry, clean up

```sh
posse workspace diff <workspace>     # what a worktree worker changed
posse workspace merge <workspace>    # merge its branch into the project directory (both sides must be clean)
posse task retry <task>              # start a new worker with the same spec, agent, model, effort and workspace
posse task close <task>              # close the worker terminal; stops the task if it is still running
posse task list                      # your tasks and their state
posse task show <task>               # spec, messages, summary, worker screen
```

1. For each `done`, check the evidence the spec asked for; for a worktree, read `workspace diff` before you merge. If something is missing or wrong, `posse task send` the worker what to fix instead of starting over.
2. For `failed`, first consider `posse task send` with what to try next if its worker is still there. For `exited`, `lost`, or a failure the same worker cannot fix, read the summary or the screen, then retry once with `posse task retry <task>` (add `--spec-file` to change the spec, `--model` or `--effort` to change the setting) or report it as unresolved. Never start an unrelated duplicate of a task.
3. Leave worker terminals open after they report: they wait at the prompt for follow-ups and the user may want to talk to them. Close a worker with `posse task close` only when the user asks.
