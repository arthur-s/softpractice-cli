<p align="center">
  <a href="https://softpractice.ru">
    <img src="https://raw.githubusercontent.com/arthur-s/softpractice-cli/main/docs/assets/softpractice-logo-horizontal-navy.png" alt="SoftPractice" width="360">
  </a>
</p>

# SoftPractice CLI

English | [Русский](https://github.com/arthur-s/softpractice-cli/blob/main/README_RU.md)

The command-line companion for [SoftPractice](https://softpractice.ru) — an
online platform for project-based programming practice with an AI mentor.

SoftPractice lets you work on realistic programming tasks in local Git
projects, submit committed solutions for review, receive feedback, and continue
through a practicum lesson by lesson. The CLI connects your local development
workflow to your SoftPractice workspace.

## Install

```bash
npm install -g @softpractice/softpractice-cli
```

Verify the installation:

```bash
softpractice --help
```

## Getting started

Sign in to SoftPractice:

```bash
softpractice login
```

Download the starter project for your current practicum:

```bash
softpractice starter
```

Open the created directory, check the project state, and read the assignment:

```bash
cd <project-directory>
softpractice status
softpractice task
softpractice material
```

Work on the project and commit your changes with Git. When the solution is
ready, submit the current commit and wait for the result:

```bash
softpractice submit --wait
```

Read the result again at any time, or open it in the browser:

```bash
softpractice result
softpractice open result
```

After an accepted solution, apply the transition to the next available lesson:

```bash
softpractice update
```

`softpractice status` always ends with the next step.

## Common commands

| Command | Description |
| --- | --- |
| `softpractice login` | Sign in through the browser |
| `softpractice starter` | Download the first lesson project |
| `softpractice status` | Show the project state and the next step |
| `softpractice task` | Show the assignment |
| `softpractice material` | Show the theory material; `--lesson ID` for another lesson |
| `softpractice hint` | Show the hints you have opened; the next one is opened on the assignment page |
| `softpractice check` | Run public checks against the current working tree |
| `softpractice submit` | Submit the current committed Git revision; `--wait` waits for the result |
| `softpractice result` | Show the evaluation result; `--wait` waits for it, `--directions` adds the reviewer's recommendations |
| `softpractice submissions` | Show the recent submissions of the lesson |
| `softpractice update` | Apply the transition to the next lesson, or move to a newer version of the current lesson (replaces only lesson files, never yours; optional until the lesson ends) |
| `softpractice open` | Open the task, material, or latest result in the browser |
| `softpractice mcp` | Run the optional local MCP lesson server for MCP clients |
| `softpractice mcp status` | Check the running MCP lesson server and its project |
| `softpractice mcp setup` | Built-in MCP setup shortcut for `claude-desktop` or `codex-desktop` |
| `softpractice mcp remove` | Remove the MCP entry from `claude-desktop` or `codex-desktop` settings |
| `softpractice project restore` | Restore a project from the latest usable revision |

Run `softpractice help <command>` for detailed help.

## Local checks

Run the public checks while working, including against uncommitted changes:

```bash
softpractice check
```

The command uses the current working tree and does not submit anything. If the
project uses a Python virtual environment, activate it first so the configured
`python3` command resolves to that environment.

To run the checks automatically against the exact committed revision before
each submission:

```bash
softpractice set auto-checks true
```

`softpractice submit --checks` enables the same pre-submission check for one
run. Unlike `softpractice check`, submission checks require a clean working
tree and run in an isolated snapshot of `HEAD`.


Lessons can declare historical data needed by their checks. The CLI downloads
and validates those inputs from your own workspace before starting the checks;
these runs require a network connection. Historical inputs are temporary and
never enter your project or submission. If the workspace advances during the
check, update the project and retry.

## Reviewer questions

When the reviewer asks questions about your solution, `softpractice result`
and `softpractice status` say so and give the link to the result page, where
you answer them.

## Machine-readable output

`status`, `task`, `material`, `hint`, `result`, `submissions`, and `submit`
accept `--json`. Each document has a `kind`; on an error the command prints a
`softpractice.error` document, also for an invalid command line (code
`usage`) and for a declined `submit` confirmation (code `declined`).

```bash
softpractice result --wait --timeout 10m --json
```

Exit codes: `0` done, `1` error, `2` invalid arguments, `3` the evaluation is
not ready yet, `4` the evaluation was superseded and will have no result.

## Working with an AI agent

The primary way to use SoftPractice with any coding agent that has terminal
access (Claude Code, Codex, Cursor, OpenCode, Antigravity, DeepSeek harness,
and others) is directly through the CLI and the starter project's root
`AGENTS.md` / `CLAUDE.md` — no MCP setup is required.

- **Direct CLI workflow:** the agent reads and drives the lesson through
  standard commands (`status`, `task`, `material`, `hint`, `check`, `submit`,
  `result`, `update`), using `--json` on supported commands (see
  [Machine-readable output](#machine-readable-output)).
- **Built-in project rules:** every starter project already includes `AGENTS.md`
  and `CLAUDE.md` (`CLAUDE.md` imports `@AGENTS.md`) in its root folder, so
  coding agents pick up the project workflow and learning boundaries
  automatically. Both files are tracked in Git, `softpractice update` never
  replaces them, and they are excluded from your submissions.
- **What stays with you:** opening new hints on the assignment page, answering
  reviewer questions on the result page, and deciding when to commit, submit,
  or apply updates.

### Quickstart prompt

Copy this prompt into your coding agent to install the CLI, sign in, download
your starter project, and inspect the first task before writing any code:

```text
Set up SoftPractice CLI and prepare my lesson project:
1. Install the CLI with `npm install -g @softpractice/softpractice-cli`. If npm is unavailable, install it with Go using the latest release version (`go install -ldflags "-X main.cliVersion=<release-version>" github.com/arthur-s/softpractice-cli/cmd/softpractice@latest`) so `softpractice version` reports a semantic version (not `dev`). Verify with `softpractice --help` and `softpractice version`.
2. Run `softpractice login`, show me the verification URL and code, and wait until I confirm sign-in in the browser.
3. Run `softpractice starter` to download the starter project. If no practicum has been started yet, tell me to open https://softpractice.ru/learning, start a practicum, and then retry `softpractice starter`.
4. Change into the created project directory, read the root `AGENTS.md`, and run `softpractice status` and `softpractice task`.
5. Briefly summarize the task requirements and stop. Do not write the solution, make a Git commit, or run `softpractice submit` until we discuss the plan.
```

## MCP server

MCP is an **optional** integration. If your coding agent already has terminal
access, working directly through the CLI and `AGENTS.md` (see
[Working with an AI agent](#working-with-an-ai-agent)) is simpler and needs no
extra setup.

Use `softpractice mcp` primarily when:

- your client has **no terminal access** (for example, Claude Desktop); or
- you want **built-in MCP confirmation dialogs** (MCP elicitation) for `submit`
  and `update`.

### 1. Connect your client

Install the CLI first (see [Install](#install)). No lesson project is needed for
this step. Choose the option that fits your client:

**Option A — Prompt your agent (easiest for any coding agent):**

```text
Register a stdio MCP server named `softpractice` in your MCP configuration:
- command: `softpractice` (or the full path from `which softpractice` / `where softpractice` if the client does not inherit the shell PATH)
- args: `["mcp", "connect"]`
Keep all other settings intact and do not start the lesson server yourself. Tell me if I need to restart the client, and remind me to run `softpractice mcp` in my lesson project directory (or `softpractice mcp --project <path>`).
```

**Option B — Desktop shortcut (Claude Desktop / Codex Desktop):**

For Claude Desktop, open the app once before running setup. After setup, quit
the desktop app completely and start it again:

```bash
softpractice mcp setup claude-desktop
# or
softpractice mcp setup codex-desktop
```

To remove the entry later (without stopping a running lesson server):

```bash
softpractice mcp remove claude-desktop
# or
softpractice mcp remove codex-desktop
```

Both commands register the CLI's native executable path and `["mcp", "connect"]`,
preserve other settings, and back up the previous file as `.bak` (see
`softpractice help mcp` or the [MCP usage guide](docs/mcp.md) for `--print` and
config file paths).

**Option C — Manual setup (universal stdio contract):**

Any MCP-compatible client connects using the same stdio contract (`command`:
`softpractice` or its full executable path, `args`: `["mcp", "connect"]`; if
your client lets you configure a tool timeout, `180` seconds is recommended):

```json
{
  "mcpServers": {
    "softpractice": {
      "command": "softpractice",
      "args": ["mcp", "connect"]
    }
  }
}
```

### 2. Start the lesson server

Sign in, then start the local lesson server for a Git project downloaded with
`softpractice starter` or restored with `softpractice project restore`:

```bash
softpractice login
cd /path/to/your/lesson-project
softpractice mcp
```

Or pass the project path from any folder:

```bash
softpractice mcp --project /path/to/your/lesson-project
```

Keep this terminal open. Why is a separate lesson server needed?

- **Project scoping and validation:** only `softpractice mcp` startup selects
  the active Git repository and verifies its SoftPractice project link. MCP
  tool calls cannot switch folders, and the server uses the CLI's saved sign-in
  without starting a browser sign-in flow itself.
- **Decoupled lifecycle:** your MCP client spawns and owns
  `softpractice mcp connect` over stdio on macOS, Linux, and Windows (no
  background daemon or startup service is installed). You can start, stop, or
  restart `softpractice mcp` without restarting your MCP client, then repeat
  the request (pending confirmations become invalid on restart).
- **Local-only security:** the lesson server listens only on `127.0.0.1:39473`
  (not an HTTP URL) and requires a random per-run key saved in the CLI's user
  configuration folder. Both processes must run on the same computer under the
  same user and share `SOFTPRACTICE_CONFIG_DIR` if it is set.

Only one lesson server can run at a time. To switch projects, stop it with
Ctrl+C and start it in the other lesson folder — no reconfiguration or client
restart is needed. `softpractice mcp` always starts the local server, including
with redirected stdin or in the background; setup never accepts `--project`.

Check which lesson server is running from any folder (no project or sign-in
needed):

```bash
softpractice mcp status
softpractice mcp status --json
```

`status` checks a live connection (a stale key file is not treated as running;
a stopped server returns `not_running`) and reports the lesson server, not the
client's `mcp connect` process. Press Ctrl+C in the server's terminal to stop
it.

### 3. Using the server

The server exposes nine tools. Ask the agent in ordinary language; it chooses
the tool and passes structured arguments. These are MCP tool names, not slash
commands. See the [MCP usage guide](docs/mcp.md) for example requests, exact
parameters, the lesson workflow, and troubleshooting.

| Tool | What it does |
| --- | --- |
| `status` | Project state and `next_actions`, each naming the tool to call next or a page for you |
| `task` | The assignment of the lesson in this folder |
| `material` | The theory material; `lesson_id` for another lesson |
| `hints` | The hints you have already opened |
| `check` | Run the public checks on the working tree |
| `submit` | Submit the committed `HEAD`, after you confirm; `wait_seconds` waits for the result |
| `result` | The evaluation result; `wait_seconds` waits for it |
| `submissions` | The recent submissions of the lesson |
| `update` | Apply the lesson update, after you confirm the list of files it changes |

The task and the material are also available as the resources
`softpractice://lesson/task` and `softpractice://lesson/material`.

What the server leaves to you:

- `submit` and `update` ask you to confirm in the client, with the commit or
  the list of files. If the client cannot ask (it does not support MCP
  elicitation), they do nothing and return `confirmation_unavailable`; run
  `softpractice submit` or `softpractice update` in a terminal instead.
- Results never include the reviewer's recommendations (`--directions`) or
  the text of the reviewer's questions. You answer the questions on the result
  page; the agent gets their number and the link.
- The agent sees only the hints you have opened; you open the next one on the
  assignment page.
- `check` runs only the checks the server publishes for the lesson. If
  `.softpractice/checks.json` differs, it returns `checks_not_published`:
  review the commands and run `softpractice check` in a terminal.
- `login`, `logout`, `starter`, `project restore`, `set`, and `open` are not
  available through MCP.

These limits apply to the MCP tools. An agent that can run shell commands can
also run the `softpractice` commands themselves.

Errors are tool results with `isError` and a JSON body
`{"error": {"code", "message", "action"}}`; `action` says what you have to do,
for example `login_required` asks you to run `softpractice login`.

Tool names and descriptions are in English, for the model. Messages meant for
you, such as the confirmation and error messages, use the CLI language (see
below); when no language is configured, the MCP server uses Russian, the
language of the course. To change it, set `SOFTPRACTICE_LANGUAGE` in the
server's `env` in the client configuration, or pass `--lang en` before `mcp`.

## Language

SoftPractice CLI supports English and Russian and selects a language from the
system locale when possible.

Choose a language explicitly:

```bash
softpractice set lang en
softpractice set lang ru
```

You can also override it for a single command:

```bash
softpractice --lang ru status
```

## Project recovery

If the original project directory is unavailable, restore the latest usable
revision into a new directory:

```bash
softpractice project restore \
  --practicum <practicum-id> \
  --directory ./restored-project
```

The CLI never overwrites an existing project directory.

## Links

- [SoftPractice](https://softpractice.ru)
- [Source code](https://github.com/arthur-s/softpractice-cli)
- [Issues](https://github.com/arthur-s/softpractice-cli/issues)

## License

[MIT](LICENSE)
