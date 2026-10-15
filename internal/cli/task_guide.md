# Hive tasks: dispatch work to other agents

Use `hive task` when part of the work should be done by another agent (claude, codex or grok) while you coordinate. Each worker runs in its own Hive terminal that the user can watch. Workers report back through Hive, so never scrape their terminals for results.

## Dispatch

```sh
hive task start --agent grok --title "short title" --spec-file - <<'SPEC'
<self-contained task spec>
SPEC
```

- The worker starts in your workspace. `--workspace <name>` picks another one; `--worktree <name>` (git projects only) gives the worker its own git worktree and branch, so parallel edits never collide.
- Start every independent task before you wait.
- Workers cannot see your conversation. A spec must stand on its own: the target (files, devices, environment), the result you want, constraints and what not to touch, and the evidence that proves it is done.

## Wait for results

```sh
hive task wait     # blocks until a worker finishes, asks or exits; up to 10 minutes, so give your shell tool a 10-minute timeout
hive task inbox    # the same without blocking
```

You may also end your turn instead of waiting: when you are idle, Hive types a reminder into your terminal ("Hive: k_… finished. Run `hive task inbox` to read."). Each message is one of:

- `done` or `failed`: the worker's summary and report file. Check what matters before you rely on it.
- `question`: the worker is blocked until you answer with `hive task reply <message-id> "<answer>"`. Ask the user only when you cannot decide.
- `exited`: the worker stopped without reporting. `hive task show <task>` includes the end of its screen.

## Steer and clean up

```sh
hive task send <task> "<extra instruction>"   # the worker reads it at its next checkpoint
hive task list                                # your tasks and their state
hive task show <task>                         # spec, messages, summary, worker screen
hive task close <task>                        # close the worker terminal; stops the task if it is still running
hive workspace diff <workspace>               # what a worktree worker changed
hive workspace merge <workspace>              # merge its branch into the project directory (both sides must be clean)
```

After you accept a result, close that worker unless the user wants to keep it. Report to the user per task: the outcome, the evidence, and anything unresolved.
