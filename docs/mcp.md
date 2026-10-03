# Using the SoftPractice MCP server

English | [Русский](mcp_ru.md)

Connect the server using the [README instructions](../README.md#mcp-server).
It works with one linked lesson Git project, selected by its startup directory
or `--project`. Tool calls cannot switch the project folder.

Setup (`softpractice mcp setup ...`) works from any folder. Then run
`softpractice mcp` in the lesson folder or pass `--project DIR`, and keep the
terminal open. The client connects through `softpractice mcp connect`. To
switch projects, stop the server, start it in the other folder, and reconnect
the client; setup does not need to be repeated. Clients can also start a
direct server with `--stdio`.

## What to ask the agent

Write your request in the chat with Codex, Claude, or another MCP client. The
agent translates it into tool calls; the server accepts structured calls, not
free-form text or shell commands. You do not need to memorize the tool names.
Mention “SoftPractice MCP” when the agent has several sources of information.

| Example request | Tool |
| --- | --- |
| “Use SoftPractice MCP to show my lesson status and what I should do next.” | `status` |
| “Show the current assignment and its requirements.” | `task` |
| “Show the theory material for this lesson.” | `material` |
| “Show the material for pa-foundation-01.” | `material` with `lesson_id` |
| “Show the prepared hints I have already opened.” | `hints` |
| “Run the lesson's public checks on my current files.” | `check` |
| “Submit my committed solution for review and wait up to 45 seconds.” | `submit` with `wait_seconds` |
| “Show the latest evaluation result.” | `result` |
| “Wait up to 45 seconds for the evaluation.” | `result` with `wait_seconds` |
| “Show my recent submissions for this lesson.” | `submissions` |
| “Apply the pending lesson update after showing me the file changes.” | `update` |

For example, start with:

> Use SoftPractice MCP to read my status, task, and material. Explain the
> requirements and help me plan my work. Let me write the solution myself.

The wording is flexible. `status` returns `next_actions` with a tool and its
arguments, a Git command, or a page URL. Ask the agent to follow those actions;
some steps need your input on the website.

## Supported tools and parameters

All parameters are optional. Tools without parameters receive `{}`. The JSON
examples below are tool arguments, not text to paste into a terminal.

| Tool | Arguments | Result and conditions |
| --- | --- | --- |
| `status` | `{}` | Account, linked lesson and version, Git HEAD, clean/dirty tree, latest submission, and `next_actions`. Read it first and after each step. |
| `task` | `{}` | The assignment pinned to the lesson version in this folder; also indicates a pending transition or version update. |
| `material` | `{"lesson_id":"pa-foundation-01"}` or `{}` | Theory for the specified lesson, or the folder's lesson by default. `material` is `null` if no theory is available. |
| `hints` | `{}` | Only the hints already opened. `status` is `available`, `exhausted`, or `unavailable`; `url` points to the assignment page. |
| `check` | `{}` | Runs published public checks on the working tree, including uncommitted changes. Returns `passed`, output, and a failure description when applicable. |
| `submit` | `{"wait_seconds":45}` or `{}` | Sends committed HEAD after your confirmation. Requires a clean tree and a submittable lesson. Does not run local checks. Returns submission IDs and the result URL; with a wait, also the evaluation or a waiting error. |
| `result` | `{"submission_id":"<canonical UUID>","wait_seconds":45}` or `{}` | Reads a specified submission, or the latest by default. Returns `ready`, `pending`, or `superseded`. Immediately after a transition, the latest result may belong to the previous lesson (`previous_lesson_id`). |
| `submissions` | `{}` | Up to five recent submissions of the lesson, newest first. |
| `update` | `{}` | Applies a pending transition, lesson version update, or missing public checks configuration. Requires a clean tree; shows the file operations for your confirmation and commits the update to Git. |

`wait_seconds` is an integer from **0 to 600**. Omitting it or using `0` returns
without waiting for the evaluation. Use a value below your client's tool
timeout; **45 seconds** is the server's suggested wait. If the outcome is
`pending`, call `result` again, respecting `next_poll_seconds` when provided.
`ready` means the evaluation finished; inspect the result to see whether the
solution was accepted, needs revision, or encountered a technical failure.
`superseded` means this evaluation will have no result; submit a new commit.

`check` keeps the last 32 KiB of combined stdout/stderr. When output was cut,
`truncated` is `true`. Failed checks return `passed: false`; inability to run
the tool, such as missing authorization, is a tool error.

The assignment and theory are also available as Markdown resources:

- `softpractice://lesson/task`
- `softpractice://lesson/material`

Resource access depends on the client. The `task` and `material` tools provide
the same content without requiring a resource browser.

## A lesson session

1. Read `status`, then `task` and `material`. Optionally read opened `hints`.
2. Work on your solution. Editing files and making Git commits are separate
   editor or shell actions; the MCP server has no tools for them.
3. Run `check`. It sees your uncommitted work. When ready, commit all changes.
4. Call `submit` and review the confirmation showing the lesson and commit.
   You must confirm in the client's confirmation UI; saying “yes” in the chat
   does not replace that confirmation.
5. Read `result`, or wait for it with `wait_seconds`. If waiting after a
   submission fails, the submission may already have been sent: use its ID
   with `result` instead of immediately submitting again.
6. Read the feedback. Answer reviewer questions yourself on `result_url` when
   `questions_answerable` is true. After acceptance, read `status` and apply
   `update` when offered. Review its file list before confirming.

## What remains outside MCP

The server does not expose `login`, `logout`, `starter`, `project restore`,
`set`, or `open`. Ask for these and the agent may suggest a terminal command
or use its separate shell/browser tools; they are not SoftPractice MCP calls.

Open the next hint on the assignment page yourself. The MCP server cannot
open hints, return the reviewer's directions (`--directions`), or return the
text of reviewer questions. It provides findings and the number of questions;
read and answer the questions on the result page. The server instructions
also tell the agent not to obtain those texts through the CLI or answer the
questions for you. These boundaries guide the agent; they do not disable its
separate shell tools.

## When something does not work

| Symptom or code | What to do |
| --- | --- |
| Bridge reports that the server is unavailable | Start `softpractice mcp` in the lesson folder or pass `--project DIR`, then reconnect the MCP client. |
| Server does not start | Check that the client can find the installed CLI; use its executable's full path if needed. Check `--project` points to an existing folder. |
| `not_git_repository` or `project_not_linked` | Select the lesson Git project, not the parent directory. Download or restore the project with the CLI if needed. |
| `login_required` | Run `softpractice login` in a terminal on the same computer and retry the tool. |
| `checks_not_published` | Compare `.softpractice/checks.json` with the lesson's published checks. Apply an offered update, or inspect the commands and run `softpractice check` in a terminal. |
| `confirmation_unavailable` | The client cannot ask for confirmation through MCP elicitation. Run `softpractice submit` or `softpractice update` yourself in a terminal. |
| `declined` | You declined or dismissed the confirmation; nothing was sent or applied. Call the tool again when ready. |
| `changed_since_confirmation` | The project or lesson changed while you were confirming. Read `status` and call the tool again to review a new confirmation. |
| Tool timed out | Reduce `wait_seconds` or increase the client's tool timeout. Check `status` or `result` before retrying `submit`. |

Tool errors carry `isError` and a JSON error with `code`, `message`, and,
when applicable, `action`. API errors can also include `http_status`,
`api_code`, and `support_id`. Keep the support ID when reporting a problem.
