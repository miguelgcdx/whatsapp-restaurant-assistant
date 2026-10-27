# Project agent instructions

## Project-local Gentle Shell

Run `./scripts/gentle-shell-local [arguments...]` to start a **new** Gentle Shell
process with an absolute `TMPDIR` in a private, unique directory under this
repository's existing `backend/.cache/tmp` (owned by you, mode `0700`). The
wrapper resolves the repository dynamically and invokes the `gentle-shell`
executable on `PATH`, not a shell function. Missing or unsafe storage fails
without fallback; launch directories are retained, not automatically cleaned.
This does not retrofit the live parent's environment, modify global shell
hooks, or itself retry any stopped verification. Activate it only by starting
that new process; existing sessions remain unchanged.

## Temporary storage

- Use project-local, disk-backed temporary storage instead of `/tmp` for task
  files, test databases, verification copies, and agent-created scratch files.
- Resolve the repository root dynamically with `git rev-parse --show-toplevel`.
  Stop if root resolution fails; do not silently fall back to `/tmp`.
- This repository already uses `backend/.cache`, which is excluded from Git.
  Use `<repository-root>/backend/.cache/tmp` as the temporary-storage base.
- Create the base directory if absent, with permissions `0700`. Do not adopt a
  symlink or a directory owned by another user.
- Create a unique private subdirectory for each run. Set `TMPDIR` to its absolute
  path explicitly for every relevant command, subprocess, and subagent, including
  commands that replace rather than inherit the environment.
- A typical shell setup is:

  ```sh
  PROJECT_ROOT="$(git rev-parse --show-toplevel)" || exit 1
  TMP_BASE="$PROJECT_ROOT/backend/.cache/tmp"
  # Check ownership and reject symlinks before using an existing directory.
  mkdir -p -m 700 "$TMP_BASE"
  RUN_TMP="$(mktemp -d "$TMP_BASE/run-XXXXXX")" || exit 1
  export TMPDIR="$RUN_TMP"
  ```

- The shell example does not change the environment of later independent tool
  calls. Reapply the absolute `TMPDIR` in those calls and pass it to subagents.
- Do not relocate `HOME` or tool caches unless the task requires isolated values;
  when required, place newly owned directories inside the project cache as well.
- If a tool ignores `TMPDIR` or requires another temporary location, explain the
  exception before proceeding. Do not assume this policy is a system sandbox.
- Shut down owned processes before removing their disposable run files. Never
  delete another run's files, retained diagnostic evidence, or pre-existing data.
- Temporary-location changes do not authorize retrying a previously stopped
  verification, changing resource limits, or deleting old `/tmp` evidence.
