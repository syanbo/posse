# Hive tasks: coordinate other agents

`hive task` dispatches work to other agents (claude, codex, grok or cursor), each in its own Hive terminal the user can watch, and brings their results back to you. Workers report through Hive; never scrape their terminals for results.

**Done means:** every task you started has one explicit outcome, every worker terminal is closed or deliberately kept, and your report to the user names, per task, the outcome, the evidence and anything unresolved.

## Pick your role

- The user wants work supervised, parallelized or fanned out, with results coming back to you: you are the coordinator; use `hive task` as below.
- The user only wants another agent to take something over: start it with `hive terminal create <workspace> --agent <name> --prompt "..."` or `hive workspace create <project> <name> --agent <name> --prompt "..."`. Create no task and do not wait for it.
- `HIVE_TASK_ID` is set: you are a worker. Follow the instructions you were started with; you may dispatch sub-workers only for work inside your own task.

## Dispatch

```sh
hive task start --agent codex --title "short title" [--worktree <name>] --spec-file - <<'SPEC'
<self-contained task spec>
SPEC
```

A spec must stand on its own; the worker cannot see your conversation. Name:

- **Target:** the files, component, device or environment in scope.
- **Result:** the concrete change or answer to produce.
- **Constraints:** what must stay true and what not to touch.
- **Ownership:** which files the worker may edit.
- **Acceptance:** the test, output or evidence that proves it is done.

Start every independent task before you wait. Work with an order runs in waves: start the next wave after the one it depends on has settled. Parallel workers that edit code each get `--worktree <name>` (git projects only), so their edits never collide. Workers nest at most 3 deep; at the limit, do the work yourself. `--workspace <name>` starts a worker in another workspace.

## Wait and answer

```sh
hive task wait     # blocks until a worker finishes, asks or exits; up to 10 minutes, so give your shell tool a 10-minute timeout
hive task inbox    # the same without blocking
```

You may also end your turn instead: when you are idle, Hive types a reminder into your terminal ("Hive: k_… finished. Run `hive task inbox` to read."). Each message is one of:

- `done` / `failed`: the worker's summary and report file. Check what matters before you rely on it.
- `question`: the worker is blocked until you run `hive task reply <message-id> "<answer>"`. Decide yourself when you can; ask the user only when you cannot.
- `exited`: the worker stopped without reporting. `hive task show <task>` includes the end of its screen.

A wait that times out or returns nothing is a checkpoint, not a failure: keep waiting. After three empty waits in a row, look with `hive task list` and `hive task show <task>`. Only a worker that has exited (`exited` or `lost`) or reported `failed` may be retried or given up on; silence alone is not proof.

`hive task send <task> "<extra instruction>"` reaches the worker at its next checkpoint; sending does not mean it has read it.

## Accept, retry, clean up

```sh
hive workspace diff <workspace>     # what a worktree worker changed
hive workspace merge <workspace>    # merge its branch into the project directory (both sides must be clean)
hive task retry <task>              # start a new worker with the same spec, agent and workspace
hive task close <task>              # close the worker terminal; stops the task if it is still running
hive task list                      # your tasks and their state
hive task show <task>               # spec, messages, summary, worker screen
```

1. For each `done`, check the evidence the spec asked for; for a worktree, read `workspace diff` before you merge.
2. For `failed`, `exited` or `lost`, read the summary or the screen, then retry once with `hive task retry <task>` (add `--spec-file` to change the spec) or report it as unresolved. Never start an unrelated duplicate of a task.
3. Close each worker once you have accepted its result, unless the user wants to keep it; say which ones you kept and why.
