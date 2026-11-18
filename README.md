# tubas

A chat with AI in your terminal that grows into a team of agents. tubas is its own agent harness: it runs every agent itself, with its own tools, on the model provider you choose.

- **Just talk:** type `tubas` and ask. It starts as a plain chat with one assistant. The first time, it asks for a key (`/connect`), or uses your company's gateway from your Claude settings on its own.
- **Your team, when you want one:** agents with a role, a model and their own tools hand work to each other with @mentions. You follow it all in a quiet chat and approve anything that touches the real world.
- **A real harness:** answers stream as they are written; agents read, search and edit code in their own git worktree, run commands (each one waits for your `y`), keep a checklist and read web pages. Each agent remembers the whole conversation, and a long one compacts itself.
- **Any provider:** Anthropic (an API key or your company's gateway), DeepSeek, GLM, Kimi, Qwen, OpenRouter, OpenAI, Gemini, a local Ollama, or your own OpenAI-compatible server.
- **Terminals next to the chat:** your shell, a dev server or any command runs in a real terminal pane, in its own git worktree if you want.
- **It keeps working when you leave:** a background server holds the sessions, routines and panes. Close the window and come back later, from any terminal, by typing `tubas`.

It is easy to use, everything has a screen (no file needs editing by hand), and being a terminal app makes it light.

The name comes from the whale shark (*tubarão-baleia*): the biggest fish in the sea, always swimming with a school around it. Its mascot is the tubaleia, half shark and half whale shark.

![A plain chat: the whole answer, rendered](docs/chat.png)

![/model lists what your company gateway serves](docs/model.png)

![Panes: terminals next to the chat, tabs in the sidebar](docs/panes.png)

## Install

You need [Go](https://go.dev) 1.26 or newer.

```bash
go install .
```

That puts `tubas` in Go's bin folder (`~/go/bin`), which the Go installer adds to your PATH. From then on, type `tubas` in any terminal:

- inside a team folder, or any folder below it, it opens that team, the way git finds its repository;
- anywhere else, it opens your personal team in `~/.tubas/personal`, made the first time;
- `tubas init DIR` makes a new team for a project.

## Quick start

Type `tubas` anywhere and ask something. That's it: your personal tubas is a chat with one assistant.

- **The first time,** when no model can be reached, tubas opens "Welcome to tubas": pick a provider, paste its key, and you're in. A company gateway in your Claude settings needs nothing: it just works.
- The answer appears while it is written, with headings, lists, code and tables; `pgup` `pgdown` (or the wheel) scroll back.
- Keep typing to continue the same conversation. `/new` (or `ctrl+n`) starts another; `ctrl+g` lists them all.
- `/model` (or `ctrl+o`) picks the model: every one your provider serves. Type to filter, `enter` to pick.
- `/agents` adds a teammate (an analyst, an engineer, a reviewer…). With two or more, they hand work to each other with @mentions and you see who does what.
- Type `/` to see every command, `?` for the keys.

### Connecting a model: `/connect`

`/connect` (or `/login`) lists the providers and how each one is reached: your company gateway (from your Claude settings), a key in an environment variable, a key you saved here, or nothing yet. Pick one and paste its key: tubas checks it by asking the provider for its models, then keeps it in your system's keychain (Windows Credential Manager, the macOS Keychain, the Secret Service on Linux), or, where there is none, in a file only you can read in your user folder. Keys never go into a team folder, and never travel over the background server's API.

- **+ your own server** adds any OpenAI-compatible address (vLLM, LM Studio, LiteLLM…), with its model and an optional key.
- `d` twice forgets a saved key, or a server of yours.
- Which key wins: your Claude settings (a gateway, its token or `apiKeyHelper`), then the provider's environment variable, then the key saved here.
- Connecting a provider while your assistant can't reach its own switches the assistant to it: connecting is all it takes to start.

**Why not my Claude subscription?** Since February 2026 Anthropic allows the Claude Pro and Max logins only in Claude Code and claude.ai, so tubas, like opencode, offers no such login. Use an API key (billed per use), your company's gateway, or another provider.

### When an answer doesn't come

```bash
tubas doctor
```

It checks, one step at a time, how agents reach a model on this computer: the keys each provider uses, your Claude settings (a company gateway, its token script, its model list), one real answer from every provider your agents use, the shell agents run commands in, and agent files from before. Each step says what it found and, when it fails, what to do; the output has no keys, so you can share it.

While an answer is coming, the chat says when the provider is failing (`⟳ the provider failed (529) · trying again, 3 of 9`) instead of a silent "working". tubas retries 408, 409, 429 and 5xx answers and dropped connections, waiting what the provider asks (`retry-after`). An expired token from your `apiKeyHelper` is fetched again once. A gateway that refuses a field (thinking, effort, cache) gets the request without it, and tubas remembers that for that gateway.

### An example team

From this repository's folder, open the example team, a data team over a synthetic delivery database:

```bash
tubas -team examples/dados
```

1. Type `por que os pedidos caíram em março?` and press `enter`. A question that names no one goes to the team's lead (Iris here); write `@leo` anywhere in it to pick someone else.
2. When an agent wants to publish a report, the bar at the bottom waits for `y` (approve) or `n` (deny).
3. Press `ctrl+b` then `v`, then `enter`: your shell opens next to the chat. `ctrl+b` then `h` goes back to the chat.
4. Press `ctrl+b` then `q` to leave. Everything keeps running; `tubas` brings it back.

`tubas -team examples/dados -engine fake` runs the same team with scripted agents: no model calls, for offline demos.

## A team

A team is a folder:

```
my-team/
  team.yaml       name, lead, routing, hop limit, connectors
  agents/*.md     one file per agent
  routines.yaml   agents on a schedule (optional)
```

`team.yaml` says where tools come from. A **connector** is built into tubas, a git repository, or any [MCP](https://modelcontextprotocol.io) server, over stdio or HTTP. The easy way is `/mcp` in the chat: it lists the MCP servers your Claude Code setup already has (yours, the project's, `.mcp.json`) and adds one with `enter`, as `claude: NAME`, so tubas reads it from there each time and its tokens stay there. By hand:

```yaml
name: support
example: "what changed in last week's tickets?"
lead: ana                 # answers when nobody else is picked (default: the first agent)
routing: router           # questions with no @: lead (default), router or all
max_hops: 6
notify: all               # all (default), window or off: see Notifications
provider: deepseek        # agents that name no provider use this one (default: anthropic)
allow_commands:           # shell commands that run without asking you
  - go test
  - npm run lint

connectors:
  db:                       # built in: read-only SQL over a SQLite file
    builtin: sqlite
    db: data/tickets.db
  reports:                  # built in: publish markdown reports to a folder
    builtin: reports
    dir: reports
    channel: "#support"     # the channel agents publish to by default
  api:                      # a git repository: see Code
    repo: E:/code/api
  github:                   # any MCP server tubas can start
    command: npx
    args: ["-y", "@modelcontextprotocol/server-github"]
    env: { GITHUB_PERSONAL_ACCESS_TOKEN: "${GITHUB_TOKEN}" }
    read: [search_issues, get_issue]   # optional: treat these as read-only
    write: [merge_pull_request]        # optional: treat these as writes
  docs:                     # an MCP server over HTTP (streamable HTTP)
    url: https://mcp.example.com/mcp
    headers: { Authorization: "Bearer ${DOCS_TOKEN}" }
  playwright:               # one of your Claude Code MCP servers, by name
    claude: playwright
```

Each agent file gives the agent an access level per connector:

```markdown
---
name: ana
role: analyst
color: orange           # orange, blue, purple, green, pink or cyan
provider: deepseek      # where its model is served: anthropic (default), deepseek, glm, your own…
model: deepseek-v4-pro  # on anthropic: haiku, sonnet, opus, or your gateway's names
effort: medium          # low, medium, high; parallel turns default to low
tools:
  db: read              # none, read or write
  github: write
can_call: [bia]         # who it may hand off to
token_limit: 50000      # past this, the conversation is compacted
---
You are Ana, the support analyst...
```

- **none**: the agent does not see the connector.
- **read**: it sees only the tools that read. A tool reads when its server marks it so (`readOnlyHint`) or when `team.yaml` lists it under `read`.
- **write**: it sees every tool, and each one that writes waits for your `y` before it runs.

You rarely write these files by hand: agents › `n` makes an agent from a template, `enter` edits one, `p` opens the permissions table. An agent file from before may have a `harness:` line: it is read and ignored (tubas runs every agent itself), and goes the next time the agent is saved.

### Who answers

| You write | Who answers |
| --- | --- |
| `@leo is the load ok?` (or `@leo` anywhere) | Leo |
| `@todos what would you check first?` | everyone at once; whoever has nothing to add passes, and the lead closes the round |
| `is the load ok?` | the team's `routing`: the **lead**, a **router**, or **all** |

The router follows [agentes.chat's design](https://github.com/agentes-chat/agentes-chat/blob/main/docs/adr/0013-router.md): code picks the candidates, one cheap call to the team provider's fast model (haiku on Anthropic) scores them, and code decides. It picks one agent, or several when the question speaks to the whole team; when nobody fits, the lead answers. The screen shows the pick and why (`→ router: @leo 0.80 · pipeline question`). Agents brought in this way may answer `PASS`; agents you name always answer.

### Workspaces of the agents

Every agent gets a folder of its own that survives between sessions, and the team gets a shared one, through the built-in `workspace` connector:

```
my-team/.tubas/workspaces/ana/   ana reads and writes here, no approval needed
my-team/.tubas/shared/           everyone reads and writes
```

Agents read each other's folders (`@leo/notes.md`) but write only in their own and in `shared/`. Paths cannot leave these folders. Set `workspace: read` or `none` in an agent's tools to limit it.

## The harness

tubas runs the agent loop itself: it sends the conversation and the tools to the model, runs the tools the model calls, sends the results back, until the agent answers.

**Its own tools,** run inside the turn (they keep the shell's folder between commands, remember which files the agent read, stop with the turn and ask you directly):

| Tool | What it does |
| --- | --- |
| `read` | a file with line numbers, in parts (2000 lines at a time); refuses binary files |
| `write` | a whole file, in the agent's worktree only; an existing file must have been read first and be unchanged since |
| `edit` | exact text replaced, once (or every time with `replace_all`); keeps Windows line ends |
| `glob` | files by name (`**/*.go`), newest first, leaving out what `.gitignore` ignores |
| `grep` | file contents by regular expression, with [ripgrep](https://github.com/BurntSushi/ripgrep) when it's installed |
| `bash` | a command in the agent's worktree: bash (Git Bash on Windows, PowerShell when there's none), the folder kept between commands, stopped after 2 minutes (10 at most) with everything it started. **Every command waits for your `y`,** except those `allow_commands` allows (anything chained after them, `;` `&&` `|` `$(`, asks again) |
| `todo_write` | a checklist of the task, shown on the agent's turn |
| `web_fetch` | a web page as text; you allow each site once per conversation |

Files and the shell exist only for agents with a repository (see Code); everyone gets the checklist and the web. The team's connectors (SQL, reports, MCP servers…) come through the tubas gateway, an MCP server started for the turn, which lists only what the agent's access allows and holds every write until you approve it.

**Streaming and retries.** Text reaches the screen as it is written. A failing provider is tried again, up to 10 times, and each attempt shows. A stream cut half-way takes its text back and starts again. An answer cut by the output limit carries on, a few times.

**Thinking and effort.** On models that take them, agents think adaptively (`thinking: adaptive`), and `effort:` in the agent file is sent as is. The model table (`internal/llm/models.go`) knows each Claude model's context, output limit and prices.

**Memory.** Each agent keeps its whole side of a conversation (questions, answers, tool calls and results) in `.tubas/conversations/<conversation>/<agent>.jsonl`, append-only. A follow-up continues it, told only what happened since it last spoke. The file is never edited: thinking blocks are sent back exactly as they came, as the models require. Deleting a conversation deletes its histories.

**Compaction.** When a request grows past the agent's `token_limit` (or most of the model's context, up to 150K tokens), the model summarizes the conversation and the next requests start from the summary, between tool rounds, never inside one. The chat shows `compact(the conversation is long)`.

**The prompt cache.** The system prompt and tools stay the same within a conversation, and each request asks for the cache, so earlier steps cost a fraction (`34.9k tok · 58% cached · $0.005`).

## Providers

An agent's **provider** is where its model is served. One team can mix them: Iris on your company's Claude gateway, Leo on DeepSeek, Nico on a local Ollama, with the same tools, handoffs and approvals.

| Provider | Endpoint | Key |
| --- | --- | --- |
| `anthropic` (default) | your company gateway from your Claude settings, else Anthropic's API | the gateway's token or `apiKeyHelper`, `ANTHROPIC_API_KEY`, or `/connect` |
| `deepseek`, `glm`, `kimi`, `qwen` | their Anthropic-compatible endpoints | `DEEPSEEK_API_KEY`, `ZAI_API_KEY`, `MOONSHOT_API_KEY`, `DASHSCOPE_API_KEY`, or `/connect` |
| `openrouter`, `openai`, `gemini` | their OpenAI-compatible endpoints | `OPENROUTER_API_KEY`, `OPENAI_API_KEY`, `GEMINI_API_KEY`, or `/connect` |
| `ollama` | a local Ollama | none |
| yours | any OpenAI-compatible address, added with `/connect` | optional |

`provider:` in `team.yaml` sets the team's default. A team can add providers there too; keys are named by their environment variable and never written to a file:

```yaml
providers:
  work:
    anthropic: https://llm.example.com/anthropic   # Anthropic-compatible, used first
    openai: https://llm.example.com/v1             # OpenAI-compatible
    key_env: WORK_LLM_KEY
    models: [big-model, small-model]               # first: the default, last: the router's
    prices: { big-model: [3, 0.3, 15] }            # US$ per million tokens: input, cached, output
```

What a turn cost: Claude models are priced from the model table; others from `prices:` when you give them, else the turn shows tokens only. A model that is not Claude's shows next to the turn (`· deepseek-v4-pro`).

### Your company's gateway

A work setup usually names the gateway in the `env` block of `~/.claude/settings.json` (`ANTHROPIC_BASE_URL`, a token or a script that fetches it). tubas reads your user settings and the team folder's `.claude/settings.json` and `settings.local.json`, the way [tucano](https://github.com/eduwxyz/tucano) does, and uses only what chooses and reaches the provider. The header says which one is in use: `native · gateway llm.example.com`. Nothing to configure in tubas.

- **A token from a script.** When your settings fetch the key with an `apiKeyHelper`, tubas runs it (in sh, or Git Bash on Windows), reuses the token for 5 minutes (or `CLAUDE_CODE_API_KEY_HELPER_TTL_MS`), and runs it again when the gateway says 401.
- **Your gateway's headers and model names.** `ANTHROPIC_CUSTOM_HEADERS` is sent, and `ANTHROPIC_DEFAULT_SONNET_MODEL` (and the opus and haiku ones) make `model: sonnet` mean whatever your gateway calls it. tubas also asks the gateway which models it serves (`/v1/models`, then `/models`): `/model` lists them all, next to the short names your settings map (`sonnet → acme-claude-sonnet`). When the list can't be fetched, `/model` says why, and you can still type a model name.
- **An older gateway.** A gateway that refuses a field (thinking, effort, cache) gets the request without it from then on; one that ignores streaming and answers in one piece works too.
- Bedrock, Vertex and Foundry are not reached: `/connect` another provider.

## The app

Seven sections, always in the top bar: **chat · inbox · agents · routines · connectors · files · settings**. `alt+1` to `alt+7` opens one from anywhere, or click it. Outside the chat there is nothing to type, so `1`–`7` and `tab` move between sections too, and `esc` goes back to the chat.

| Section | What it shows · keys |
| --- | --- |
| chat | the conversation |
| inbox | everything waiting for you, with a count in the top bar: approvals from any session or routine (answered in any order with `y` / `n`), routine results, skills agents suggest |
| agents | each agent: provider, model, effort, access, who it can call, skills, escalation, instructions. `n` new, `enter` edit, `p` permissions, `s` skills |
| routines | agents on a schedule: next run, last runs. `n` new, `enter` edit, `space` on/off, `r` run now, `d` delete |
| connectors | each connector, what runs it and each agent's access |
| files | what the agents wrote (workspaces, `shared/`, reports), newest first, with a preview |
| settings | routing, lead, hops, notifications, the providers connected, quiet hours, retries. `e` edit |

Every change is a form: `↑↓` moves, `←→` picks an option, letters type, `enter` on a long text (instructions, a routine's prompt, a skill) opens your `$EDITOR`, `ctrl+s` saves, `esc` cancels. Each form says which file it writes.

### Commands

Type `/` in the chat: every command, with what it does, filtered as you type (by name, alias or description). `enter` runs it.

| Command | What it does |
| --- | --- |
| `/new` (`/clear`) | a new conversation; the one on screen keeps running |
| `/conversations` (`/resume`) | your conversations, with their state (`ctrl+g`); `r` renames one, `d` deletes one |
| `/delete` | delete the conversation on screen (it asks first) |
| `/retry` · `/stop` | ask the last question again · stop the answer that is coming |
| `/copy` | copy the last answer |
| `/export [file]` | save the conversation as Markdown, in the folder you are in |
| `/model [name]` | pick the model from a list (your gateway's too), or set it at once: `/model haiku` |
| `/connect` (`/login`) | connect a provider: paste its key, or add your own server |
| `/usage` | tokens and cost of the conversation |
| `/doctor` | `tubas doctor` in a terminal next to the chat |
| `/mcp` | MCP servers: the team's (enter tests one: does it start, how many tools), and yours from Claude Code to add with `enter`; `d` removes |
| `/skills` | the skills, the team's and your Claude Code ones |
| `/<skill> [what for]` | every skill is a command: `/pdf summarize report.pdf` asks the assistant to follow it (a Claude Code skill is copied into the team first) |
| `/agents` | add a teammate |
| `/inbox` · `/routines` · `/files` · `/settings` | open that screen |
| `/terminal` | a terminal next to the chat (`ctrl+b v`) |
| `/help` · `/quit` (`/exit`) | keys and commands · close the window (everything keeps running) |

### Keys in the chat

| Key | What it does |
| --- | --- |
| `text`, `enter` | ask, or continue the conversation; in a team, `@agent` anywhere picks who answers, `@todos` asks everyone (`tab` completes) |
| `/` | the commands: `/model`, `/new`, `/agents`, `/help` |
| `pgup` `pgdown`, wheel | scroll the conversation |
| `ctrl+o` | change the model (the same as `/model`) |
| `y` / `n` | approve / deny what an agent wants to run |
| `v` | see the full call before deciding; after a session that changed code, review it |
| `↑` `↓` | scroll a plain chat; in a team, select a turn (`enter` expands or collapses it) |
| `tab` | in a team, the grid of all agents side by side |
| `esc` | back out one layer; stops the answer when nothing is open |
| `ctrl+r` | run the last question again |
| `ctrl+g` | all conversations, with their state |
| `ctrl+n` | a new conversation; the one on screen keeps running |
| `ctrl+b` then `q` | detach: close the window, leave everything running |
| `?` | keys panel |
| `ctrl+c` | quit (in a terminal pane, it goes to the program) |

Letters are always text, so a question can start with any word. Only `y`, `n` and `v` act on their own, and only while an approval is on screen. `ctrl+b` starts the pane keys, below.

### The mouse

Everything you reach with a key, you can click. `tubas -mouse=false` leaves the mouse to your terminal.

**Every hint is a button.** The lines that say a key and what it does (`enter send`, `/ commands`, `? keys`, `esc stop`, `ctrl+s save`, `esc cancel`, `n new`, `d removes`, `y deletes it for good`) do it when clicked, and so do the start screen's suggestions (`/model changes the model`). A notice never hides them: it shows where the grey text of the input is, and goes after a few seconds (one that asks, like `y deletes it`, waits for you).

| Click | What it does |
| --- | --- |
| a section in the top bar | opens it; the count at the right opens the conversation list |
| the model in the header | the model list; an agent's name (in a team) opens it among the agents |
| a command in the `/` menu, an agent in the `@` menu | runs it · picks them |
| a model in the model list, a server in `/mcp`, a provider in `/connect` | picks it · selects it (a second click adds or tests it) · connects it |
| a workspace, tab or conversation in the sidebar | shows it (a conversation opens in the chat) |
| `+ new tab`, `+ new conversation` in the sidebar | the same as `ctrl+b c` and `ctrl+n` |
| a row in a section's list | selects it; a click on the selected row opens it |
| a field in a form, an option in it | moves there · chooses that option; a long text opens your editor |
| `↓ N more lines` in the chat | back to the bottom |
| anywhere, while the keys panel is open | closes it |
| a pane | focuses it; drag over a terminal to select text, copied when you let go |
| a pane's header, twice | zooms it, twice again to go back |
| the `✕` in a pane's header | closes it (asks first while the program runs) |
| the border between two panes | drag it to resize them |
| `y` · `n` · `v` on an approval bar | approve · deny · details |
| right click on a conversation, a tab or a workspace (sidebar or list) | a menu: rename it, or delete it (it asks in the menu first) |

The wheel scrolls a terminal's history or the chat.

## Sessions and the background server

`tubas` starts a server for the team in the background (or finds the one already running), and the window attaches to it. Sessions, routines, approvals and panes live in the server, so closing the window, `ctrl+c` or `ctrl+b q` only detaches: agents keep working, routines keep running. Run `tubas` again to come back. Finished sessions are saved in `.tubas/sessions/`; tubas writes a `.gitignore` in `.tubas` so none of it goes into git.

Each question you send is a session on the server; a follow-up is a new session in the same **conversation**, so the sidebar and `ctrl+g` list conversations (titled by their first question), not every message. Opening one replays it whole. `d` in the list (or `/delete` for the one on screen) deletes a conversation for good, from the screen and from `.tubas/`, after asking; one still answering is stopped first.

Every session has a state, the same on screen and on the command line: **blocked** (waits for you), **working**, **done** (finished, not seen yet) and **idle** (seen). The top bar sums them up (`1 blocked · 2 working`), the sidebar lists the conversations with their state, and the inbox gathers what every session waits for, not only the one on screen.

![Sessions in the sidebar](docs/sessions.png)

After you update tubas, the next `tubas` replaces a server still running the old version, as long as nothing runs in it (no answer coming, nothing waiting for you, no terminal pane open); otherwise it keeps it and tells you to run `tubas stop`. On Windows, run `tubas stop` before `go install`: a running server keeps `tubas.exe` busy.

The server listens on 127.0.0.1 only, with a random token in `.tubas/server.json`. `-local` runs everything in the window instead (the fake engine and `-snapshot` always do).

## Code: repositories and a worktree per task

A git repository is a connector:

```yaml
connectors:
  api:
    repo: E:/code/api     # your checkout
    base: main            # tasks start from here (default: the branch checked out)
    setup: uv sync        # runs in every new worktree (optional)
```

Who works where is the permissions table: `api: write` for leo, `api: read` for nico.

- **read**: the agent reads, searches and lists files in your checkout (`read`, `glob`, `grep`), and changes nothing.
- **write**: the session gets its own git worktree in `.tubas/worktrees/`, on its own branch (`tubas/<session>-<title>`). The agent edits there freely (`edit`, `write`). Every `bash` command waits for your `y`, with the exact command, why the agent wants it and where it runs; "always" is not offered (`allow_commands` in `team.yaml` is the way to let some run). Anything outside the worktree is refused. Agents never commit, push or touch your checkout.

When the session ends, the chat shows what changed (`✎ api · 3 files · +12 −4 · v review`). `v` opens the review: the files and the diff; `m` merges into the base branch of your checkout (refused if your checkout is on another branch or has changes of its own), `p` pushes the branch and opens a pull request with `gh`, `d` (twice) discards the worktree and its branch.

## Panes: terminals next to the chat

A pane is a real terminal inside tubas: your shell, a dev server, a test watcher, any command. Panes live in **tabs**, and tabs in **workspaces**: the team, each repository connector, and any folder you add.

| Key | What it does |
| --- | --- |
| `ctrl+b` then `v` / `-` | a new pane to the right / below the focused one |
| `ctrl+b` then `c` | a new tab, with a new pane |
| `ctrl+b` then `a` | the new pane menu, ready for a command |
| `ctrl+b` then `h` `j` `k` `l` (or arrows) | move between panes; `o` goes to the next one |
| `ctrl+b` then `n` / `p`, `1`…`9` | next / previous tab, tab by number |
| `ctrl+b` then `w` | the next workspace |
| `ctrl+b` then `N` | a new workspace, for any folder |
| `ctrl+b` then `X` | close the workspace and its terminals, after asking; the team's own (`personal`, say) leaves the sidebar and only the chat stays |
| `ctrl+b` then `,` · `$` | rename the tab · the workspace |
| `ctrl+b` then `b` | show or hide the sidebar |
| `ctrl+b` then `z` | zoom the focused pane, again to go back |
| `ctrl+b` then `[` | scroll back through the pane (copy mode) |
| `ctrl+b` then `x` | close the focused pane (asks first while the program runs) |
| `ctrl+b` then `r` | review what a pane changed in its worktree (merge, push, discard) |
| `ctrl+b` twice | send one `ctrl+b` to the program |

The new pane menu asks what to run (your shell, or any command, flags included) and where: the workspace's folder, the folder you opened tubas from, a repository's checkout, or **a new worktree** of it on its own branch (`tubas/p2-…`), so what you run there never touches your checkout.

Keys go to the focused pane, `ctrl+c` included; tubas keeps only `ctrl+b` and `alt+1…7`. The chat is a pane too: move to it and type as usual.

**The sidebar** lists every workspace and its tabs with the state of what runs inside, and the team's conversations below them, so nothing waiting for you is out of sight. While you only chat (no terminal, no workspace of yours), it lists just the conversations. It shows up by itself once there are panes or a few sessions and the window is wide enough; `ctrl+b b` decides. Without it, a line of tabs sits above the panes.

**Copy mode** (`ctrl+b [` or the wheel) shows what the pane printed, 5000 lines back: `h` `j` `k` `l`, `w` `b`, `0` `$`, `g` `G`, `pgup` `pgdown` move; `/` searches back (`n` again, `N` forward); `v` starts a selection; `y` or `enter` copies it, or the line under the cursor; `q` goes back to the live terminal.

**States.** Each pane has a state: **blocked** when the program asks something (a `(y/n)`, a "Do you want to…"), **working** while it prints on its own, **idle** when it waits, **done** when the program exited. The pane's header, its tab in the sidebar and the top bar show them (`panes 1 blocked`).

**After a restart.** Panes run in the background server, in a pseudo-terminal (ConPTY on Windows), with a terminal emulator keeping their screen: close the window and they keep running. When the server itself restarts (`tubas stop`, a crash, the computer restarting), each program starts again in its folder (its worktree included), in the same workspaces, tabs and layout. What was running inside a shell does not come back, and a pane you closed, or whose program ended on its own, stays gone. The list lives in `.tubas/panes.json`.

## Notifications

When a pane or a session starts waiting for you, or an agent finishes, tubas tells you:

- in the window, unless you are looking at it: a notice and the terminal's bell;
- with no window open: a desktop notification (a balloon on Windows, Notification Center on macOS, notify-send on Linux).

`notify` in `team.yaml`, or settings, picks how loud: `all` (default), `window` (only in the window) or `off`.

## Agents that grow

### Model, permissions, escalation

- **Provider, model and effort** are per agent. A change applies from the agent's next turn.
- **Permissions** (agents › `p`): every agent × every connector, `space` cycles none · read · write. Writes always wait for your `y`; an agent's own workspace is free.
- **Escalation**: an agent on a cheap model can ask a stronger one for the hard step, then go on. When the same tool fails twice the advice comes with the second failure (`tool_fails`); a command or a write gets a review you see before you approve (`before_write`); or the agent calls `ask_senior` (`ask`). The senior runs on the agent's provider, gets a summary of the task's calls and errors (not the whole conversation), answers in a few lines, and shows in the chat as `↑ opus · $0.011: …`. Capped per agent and day.

```yaml
model: haiku
escalate:
  to: opus
  on: [tool_fails, before_write, ask]   # empty = all three
  per_day: 5
```

### The lead shapes the team

Every team has a `team` connector. The lead gets it with write access (others when you give it): it can **propose agents and routines** (`propose_agent`, `propose_routine`). Each proposal waits in the inbox with every field, like any write. A proposed agent can never get team access itself, and the lead can't change agents that already exist.

### Routines

`routines.yaml` in the team folder (or routines › `n`):

```yaml
quiet_hours: "22:00-07:00"   # nothing runs
retry: true                  # a failed run tries once more 5 minutes later
routines:
  - name: databricks-daily
    agent: leo
    every: weekdays 08:00     # 30m · 1h · daily 09:30 · mon,wed 10:00 · cron: 0 8 * * 1-5
    between: 08:00-20:00      # optional window, for intervals
    prompt: Check the nightly_orders job...
```

Routines run in the background server, with the window open or not. Each run is saved in `.tubas/runs/<name>/`, and its conclusion lands in the inbox (a run that answers `PASS` found nothing and stays quiet). Anything a routine wants to write waits in the inbox like the chat's approvals.

### Skills: agents learn from repeated work

When an agent does the same work (the same tools, in the same order) successfully 3 times, tubas drafts a skill from what the agent did, never from the text tools returned, and suggests it in the inbox: `y` saves it for that agent, `t` for the whole team, `e` edits it, `n` not now, `x` never. Skills live in `.tubas/skills/<name>/SKILL.md` (the Agent Skills format). An agent's prompt only carries each skill's name and when to use it; the steps load when the agent calls `load_skill`. In agents › skills (`s`) you edit them, give them to agents, archive and restore them, and add the ones already in `~/.claude/skills` (`a`), links to skill folders included (skill managers install that way). Or skip all that: every skill is a `/` command, and `/<skill>` brings a Claude Code one in when you first use it.

## Tokens

Every step of an agent resends the whole conversation, so tubas keeps both the conversation and the number of steps small. Inspired by [agentes.chat](https://github.com/agentes-chat/agentes-chat/tree/main/docs/adr) (ADRs 0043, 0050, 0051):

- **A lean, stable base.** The system prompt and tool list never change during a conversation, and each request asks for the prompt cache, so later steps read them for a fraction of the price.
- **What a tool needs, in its description.** The SQLite connector puts the schema in `run_sql`'s description, so agents skip the discovery steps.
- **Few steps.** Agents are asked to call independent tools together in one step, and reads in the same step run at the same time.
- **Small results.** A query returns at most 30 rows and 4,000 characters, with a note to aggregate or filter; a command's output keeps its start and its end.
- **Compaction.** A conversation past `token_limit` is summarized, and the next requests start from the summary.
- **Less thinking where it matters less.** Agents working in parallel, or that may pass, run with `effort: low` unless their file says otherwise.
- **Honest numbers.** The screen shows tokens, the share read from cache and the cost: `34.9k tok · 58% cached · $0.005`.

## Command line

Everything the window does, scripts and other agents can do too. Every command takes `-team DIR`; without it, tubas finds the team the same way as when you open it.

| Command | What it does |
| --- | --- |
| `tubas` | opens the team you are in, or your personal team |
| `tubas -team DIR` | opens the team in DIR |
| `tubas -resume` | reopens the team's last conversation |
| `tubas -ask "@agent ..."` | starts with that question |
| `tubas -model haiku` | every agent uses haiku (cheaper), ignoring their files |
| `tubas -engine fake` | scripted agents, no model calls |
| `tubas -local` | everything in this window, without the background server |
| `tubas -mouse=false` | leaves the mouse to your terminal |
| `tubas init DIR` | creates a new team to start from |
| `tubas seed` | recreates the team's example databases |
| `tubas status [-json]` | what runs and what waits for you: sessions, panes, routines |
| `tubas session new [-agent A] [-in CONVERSATION] "question"` | starts a session, or a follow-up in a conversation (JSON back) |
| `tubas wait ID [-until blocked,done] [-timeout 10m]` | waits for a session's state |
| `tubas read ID [-last]` | a session's events, or only its conclusion |
| `tubas answer APPROVAL-ID yes\|no` | answers an approval (`tubas status -json` lists them under `pending`) |
| `tubas pane new [-repo api [-worktree]] [-title T] [-- command ...]` | opens a pane (JSON back, with its id) |
| `tubas pane send ID "text" [-enter] [-key ctrl+c]` | types into a pane |
| `tubas pane wait ID [-until blocked,idle,done]` | waits for a pane's state |
| `tubas pane read ID` | a pane's screen as text (`-color` keeps the colors) |
| `tubas pane list [-json]`, `tubas pane close ID` | lists, closes |
| `tubas stop` | stops the server and every session and pane (panes come back with it) |
| `tubas doctor` | checks keys, your Claude settings and gateway, one real answer per provider, the shell, step by step |

## How it works

```
                    you
                     │ @agent question                     ▲ y / n
                     ▼                                     │
 ┌──────────── tubas window (Bubble Tea) ──────────────────┴───┐
 │ chat · inbox · sections · panes · sidebar · /connect        │
 └─────────────────────────────┬───────────────────────────────┘
                               │ HTTP on 127.0.0.1 (token)
 ┌──────────── tubas server, one per team ─────────────────────┐
 │ sessions (engine.Run) · routines · panes (ConPTY + emulator)│
 │                                                             │
 │ one agent turn = the native loop (engine.NativeEngine)      │
 │   ├── internal/llm: Anthropic Messages or OpenAI chat,      │
 │   │   streaming, retries, thinking, cache                   │
 │   ├── internal/tools: read write edit glob grep bash        │
 │   │   todo_write web_fetch, in the turn ──▶ your y / n      │
 │   ├── internal/thread: the agent's history, compaction      │
 │   └── tubas mcp, the gateway: only this agent's connectors  │
 │         writes ──▶ approval broker ──▶ your y / n           │
 │         ├── built in: sqlite, reports, workspace, team,     │
 │         │   skills                                          │
 │         └── any MCP server from team.yaml                   │
 └─────────────────────────────────────────────────────────────┘
```

- **Harness and orchestrator.** tubas is both: it runs each agent's loop (the harness), and decides who runs next, with which instructions and tools, and shows what happens (the orchestrator).
- **Each agent remembers.** An agent's history in a conversation is its own file; a follow-up continues it, with what the others said since. Inside a session, a handoff carries the question, a summary of each previous turn and the note it was handed.
- **Handoff protocol.** An agent passes the work on by ending its answer with `HANDOFF @name: what you need`. tubas checks the name against the agent's `can_call`; anything else is refused and shown as ✗. Sessions stop at the team's hop limit.
- **Parallel work.** Several `HANDOFF` lines fan out: those agents run at the same time, each in its own goroutine, and answer back. The caller then concludes with all their answers. This is the orchestrator-workers pattern, live on screen.
- **Permissions live in code, not in the prompt.** The tools check every path against the agent's folders, the gateway lists only what its access allows, and every write and command waits for you.
- **Isolation.** Agents see only their instructions, the team and the tools tubas gives them: your own CLAUDE.md, hooks and settings never reach them.

### Packages

| Package | What it holds |
| --- | --- |
| `internal/agent` | reads, validates and saves the agent files |
| `internal/team` | reads `team.yaml`, checks agents against the connectors, saves settings |
| `internal/engine` | the `Engine` interface, `Run` (handoffs, hop limit), the native loop, the router, escalation, providers, the approval broker, detection |
| `internal/llm` | the two wire formats (Anthropic's Messages API through the official SDK, OpenAI's chat completions), streaming, retries, the model table |
| `internal/tools` | the harness's own tools: files, search, the shell, the checklist, the web |
| `internal/thread` | each agent's history per conversation, append-only, and compaction |
| `internal/auth` | the keys you save with `/connect`: the system's keychain, or a file only you can read |
| `internal/mcp` | the gateway: built-in tools, the client for external MCP servers, approvals, escalation |
| `internal/server` | the background server: sessions, routines, panes, notifications, its HTTP API and client |
| `internal/pane` | terminal panes: a pseudo-terminal per program, a terminal emulator for its screen, its state, restore |
| `internal/repo` | git worktrees per task: create, diff, merge, push, discard |
| `internal/routine` | `routines.yaml`, schedules and saved runs |
| `internal/skill` | skills: the SKILL.md format, the learning tracker |
| `internal/notify` | desktop notifications |
| `internal/data` | the built-in SQLite connector and the synthetic delivery data |
| `internal/ui` | the window: chat, sections, forms, `/connect`, panes, sidebar, mouse |

## Development

```bash
go test ./...
```

To print one frame without opening the window (for tests and screenshots):

```bash
tubas -team examples/dados -engine fake -snapshot 128x32 -ask "@iris por que os pedidos caíram em março?" -keys "tab"
```

`-events N` stops after N engine events, to catch an agent mid-turn; `-keys` presses keys first (`v`, `y`, `tab`, `?`, `up up enter`…).

## Roadmap

- [x] Agent files, the chat, handoffs checked against `can_call`
- [x] The MCP gateway: permissions, approvals, any MCP server as a connector, general teams
- [x] Parallel handoffs, follow-ups, saved conversations, the router, `@todos`, a workspace per agent
- [x] Token savings, cost on screen
- [x] One app in seven sections: inbox, forms, permissions, routines, learned skills, escalation, the lead proposing agents and routines
- [x] The background server: detach and attach, sessions in parallel with states, routines with the window closed, the command line
- [x] Repositories as connectors, a worktree per task, review and merge
- [x] Terminal panes with workspaces, tabs, a sidebar, the mouse, copy mode, states, notifications, and panes back after a restart
- [x] tubas as its own harness: streaming, retries, its own tools, a history per agent with compaction, `/connect`, any provider
- [ ] Web search (Anthropic's server tool, or a search key) and a browser agents can drive
- [ ] Commands in the background, reading images
- [ ] Remote machines over SSH
