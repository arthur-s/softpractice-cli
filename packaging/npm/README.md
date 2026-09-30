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
| `softpractice mcp` | Run the MCP server for Claude Code, Codex, Claude Desktop, and other agents |
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

## MCP server

`softpractice mcp` is a local MCP server on stdio. It lets Claude Code, Codex,
Claude Desktop, and other MCP clients work through the lesson with you, using
the same operations as the commands above. Sign in first with
`softpractice login`: the server never starts a sign-in itself.

The server works on the lesson project in the directory the client starts it
in. When the client starts it elsewhere, as Claude Desktop does, pass the
project folder with `--project`.

**Claude Code**, from the project folder:

```bash
claude mcp add softpractice -- softpractice mcp
```

**Codex**, in `~/.codex/config.toml`:

```toml
[mcp_servers.softpractice]
command = "softpractice"
args = ["mcp"]
# Allow result and submit to wait for an evaluation.
tool_timeout_sec = 180
```

**Claude Desktop**, in `claude_desktop_config.json`. Use the full path of the
`softpractice` executable if the app does not find it on `PATH`:

```json
{
  "mcpServers": {
    "softpractice": {
      "command": "softpractice",
      "args": ["mcp", "--project", "/path/to/your/lesson-project"]
    }
  }
}
```

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
