# Project agent instructions

## Project-local Gentle Shell

The optional `./scripts/gentle-shell-local [arguments...]` wrapper starts a **new**
Gentle Shell process with an absolute `TMPDIR` in a private, unique directory
under this repository's existing `backend/.cache/tmp` (owned by you, mode `0700`).
That wrapper retains its project-local behavior; it is not required for temporary
storage and does not implement the current `/tmp` preference. The
wrapper resolves the repository dynamically and invokes the `gentle-shell`
executable on `PATH`, not a shell function. Missing or unsafe storage fails
without fallback; launch directories are retained, not automatically cleaned.
This does not retrofit the live parent's environment, modify global shell
hooks, or itself retry any stopped verification. Activate it only by starting
that new process; existing sessions remain unchanged.

## Temporary storage

- Prefer `/tmp` for task files, test databases, verification copies, and
  agent-created scratch files.
- Create a unique, owned private directory with permissions `0700` for each run.
  Do not adopt a symlink or a directory owned by another user.
- Set `TMPDIR`, `TMP`, and `TEMP` coherently to that directory's absolute path
  explicitly for every relevant command, subprocess, and subagent, including
  commands that replace rather than inherit the environment.
- A typical shell setup is:

  ```sh
  RUN_TMP="$(mktemp -d /tmp/restaurant-assistant-XXXXXX)" || exit 1
  chmod 700 "$RUN_TMP" || exit 1
  export TMPDIR="$RUN_TMP" TMP="$RUN_TMP" TEMP="$RUN_TMP"
  ```

- The shell example does not change the environment of later independent tool
  calls. Reapply the absolute `TMPDIR`, `TMP`, and `TEMP` in those calls and pass
  them to subagents.
- Do not relocate `HOME` or tool caches unless the task requires isolated values;
  when required, use newly owned private directories.
- If a tool ignores `TMPDIR` or requires another temporary location, explain the
  exception before proceeding. Do not assume this policy is a system sandbox.
- Shut down owned processes before removing their disposable run files. Never
  delete another run's files, retained diagnostic evidence, or pre-existing data.
- Temporary-location changes do not authorize retrying a previously stopped
  verification, changing resource limits, or deleting old `/tmp` evidence.
