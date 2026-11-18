# Design

How `airo`'s commands are shaped, for people and coding agents adding or
changing one. `airo` may be the only tool a person has, so it covers both
daily tasks and managing resources, without kubectl.

## Grammar

**Every command is `airo <noun> [verb] [argument]`**, like `gh issue list`.
The noun is a kind of resource. The docs and the help write it in the
singular, and the plural works too.

```sh
airo tunnel 3000             # the noun's default task
airo tunnel list             # a verb
airo tunnel delete my-app
airo agent "fix the build"   # the default task of another noun
```

| Part | Rule |
| --- | --- |
| Noun | A kind of resource, such as `tunnel` or `agent`. `tunnels` and `agents` mean the same |
| No verb | The noun's default task, if it has one. Otherwise the help |
| `list`, `get`, `delete` | Work for every kind the cluster serves, also kinds this binary does not know |
| `create` | Hand-written for each noun, with flags |
| Other verbs | Actions of one noun, such as `airo agent logs` |
| No noun | Only what belongs to no resource: `airo login`, `airo apply -f`, `airo version` |

**Every noun takes its plural.** `airo sessions list` is `airo session
list`, and `airo tunnels 3000` is `airo tunnel 3000`. A noun with its own
command names the plural as an alias, and a test fails when one does not. A
kind without a command takes the plural from the cluster. What is not a noun,
such as `airo login`, has one name.

**A noun that is not a kind in the cluster has only hand-written verbs.**
`airo session` is one: a session lives in the agents' API. It has `list`,
because people expect it, and no `get` or `delete`. Without a verb it prints
the help. `airo context` is another: a context lives in the kubeconfig.

**A noun gets a short form when people already know one**, such as `airo
ctx` for `airo context`. Add it as an alias when you add the noun.

- **Take the short form people use elsewhere**, such as kubectl's short
  names and `kubectx`. Do not invent one.
- **A noun without a known short form has none.**
- **The README and the help use the long form**, and name the short one
  once.

**`get` shows a resource's details, and `view` shows content.** `airo agent
get` prints the Agent as the cluster holds it. `airo session view` prints the
conversation.

## Default task

**A noun's default task is the one thing most people want from it**: `airo
tunnel 3000` serves a local port, `airo agent` starts a fresh session.

- **Its argument is what the task acts on, never a resource's name.** It is
  the port for a tunnel and the prompt for an agent. A resource's name is a
  flag, such as `--name` or `--agent`.
- **A noun whose task is to switch to one of its own is the exception.**
  `airo context customer` takes the context's name, because the name is all
  the task needs. `-` switches back to the previous one, as in `cd -`.
- **A verb wins over an argument.** `airo agent list` lists agents. A prompt
  that equals a verb goes after `--`, as in `airo agent -- list`.
- **A missing choice is resolved in the cluster, then by asking.** With no
  `--agent`, `airo agent` takes the project's only agent, then the one marked
  as default with the annotation `airohq.dev/default-agent: "true"`, then
  shows a picker. Where nobody can answer, such as in a script, it stops and
  lists the names. `airo agent create` finds its Harness and ModelConfig the
  same way, with `airohq.dev/default-harness` and `airohq.dev/default-model`,
  and without a picker.

## Arguments and flags

- **Everything but the one argument is a flag with a default that works**, so
  the shortest form does something useful.
- **A flag is named for what the person gets**, such as `--public` and
  `--no-auth`. The README names the field it sets.
- **kubectl's flags mean the same here**: `-n`, `--context`, `--kubeconfig`,
  and `-o yaml` on `get`, `list` and `view`.
- **A flag that names a resource is the same on every command**, such as
  `-a`, `--agent`.
- **`airo login` keeps what its flags say**: `--server`, `--context` and `-n`
  choose for one command elsewhere, and `airo login` writes them into the
  kubeconfig for every later command.

## Output

**Output starts with what the person needs next**, such as the URL.

**What a task produces goes to standard output, and notes about it to
standard error.** An agent's answer can be piped, and the session's ID does
not end up in it.

**An ID a command prints is one another command takes as it is.** The
session ID of `airo agent` and `airo session list` goes into `airo session
view`, `airo session cancel` and `airo agent -r`.

## Terminal

How `airo` looks and behaves when a person sits at it. The models are `gh`
for commands that print and end, and Claude Code and Codex for a
conversation.

**A person at a terminal gets style, and everything else gets plain text.**
A stream is styled only when it is a terminal, and never when `NO_COLOR` is
set or `TERM` is `dumb`. A pipe, a file and a script get the same words
without colour, symbols that move, or escape codes.

**Style never changes the words, and never carries meaning alone.** A colour
comes with a word or a symbol that says the same, so the output reads the
same in a pipe, in a log and to someone who does not see the colour.

**A command is interactive only when both its input and its output are
terminals.** Everything a person can choose in a picker or a prompt is also
a flag. Where nobody can answer, the command stops and names the choices.

**A command stays in the scrollback.** No full-screen mode. A conversation
keeps a live region at the bottom, for the input and the status, and what
was said scrolls up as ordinary text. On exit the live region is gone, and
what the person needs next, such as how to resume, is the last thing
printed.

**Colours are named for what they mean, in one place.** `internal/ui` holds
them, and a command writes no escape code of its own.

| Role | Used for |
| --- | --- |
| Default | Most text. What the person reads is not coloured |
| Muted | What supports the text: IDs, hints, keys, timestamps |
| Accent | The one thing to see first: the prompt mark, the selected row |
| Success, warning, error | An outcome, with `✓`, `!` or `✗` |

- **Take the terminal's own 16 colours**, so the person's theme decides the
  shades and the output reads on light and on dark.
- **Colour little.** A screen with one accent reads faster than a screen
  with five.

**Waiting is shown.** What takes longer than a moment shows a spinner on
standard error, with what it waits for and for how long. It leaves no trace
when it ends.

**An agent's text is rendered as markdown on a terminal, and is the agent's
own text in a pipe.**

**Resuming an interactive conversation restores what was said.** Both
`--resume` and `--continue` load saved prompts and answers, oldest first,
before accepting the next prompt. Use the same formatting as live chat,
and include earlier prompts in Up/Down history. Keep a draft typed during
loading, without sending it automatically. Plain terminal conversations
replay history too. A single prompt, supplied as an argument or on stdin,
prints only its new answer. Loading history is a snapshot, not a connection
to an already-running turn's stream; label saved unfinished states clearly.

**Keys mean what people know from other tools.** A line under the input
names the ones that matter now.

| Key | Does |
| --- | --- |
| Enter | Sends |
| Shift-Enter, Ctrl-J | A new line in the prompt. Ctrl-J works where the terminal does not tell Shift-Enter from Enter |
| Up, Down | The earlier prompts |
| Esc | Stops what runs now, such as the agent's turn |
| Ctrl-C | Clears the prompt. On an empty prompt, or while something runs, it asks for a second Ctrl-C |
| Ctrl-C twice | Leaves. What outlives the terminal goes on ([Cleanup](#cleanup)) |
| Ctrl-D | Ends the input, on an empty prompt |

**Leaving never stops work by surprise.** People press Ctrl-C to stop an
agent, as in other agent CLIs. While a turn runs, the first Ctrl-C says what
each key does, such as "Esc stops the agent · Ctrl-C again leaves, the agent
works on", and only a second one leaves.

**The next prompt can be drafted while a turn runs.** The input stays
editable, and Enter keeps the draft with a hint to send it after the turn
ends. Finishing or stopping a turn never sends the draft automatically.
History keeps sent prompts and restores the unfinished draft on returning
past the newest one.

**Slash commands are explicit local shortcuts in the interactive chat.**
Only the whole prompts `/help` and `/exit` are reserved: they show help and
leave the conversation. Other slash-prefixed prompts go to the agent
unchanged. Arguments and piped input are always prompts, so
`airo agent /help` sends the literal text. A slash command never adds a
second way to manage platform resources; those remain CLI commands.

**A resource looks as kubectl prints it.** `list` and `get` keep the
cluster's table: its columns, their order and their upper-case headers. On a
terminal, style may mute the header and colour a status word, and adds or
removes nothing.

- **`-o yaml` and `-o json` are data, and are never styled**, also on a
  terminal.
- **A name or an ID is never cut**, because the person copies it into the
  next command. A table that is too wide cuts free text, such as a
  description, and only on a terminal.

**Text fits the terminal's width.** Prose wraps at words, and everything
works at 80 columns.

**One library family draws everything**: Charm's Bubble Tea, Bubbles, Lip
Gloss and Glamour. No second one for pickers, prompts or colour.

### Closing messages

**The closing message tells the person how to return.** Take the concrete
resume command from [Codex's exit summary][codex-exit] and
[Claude Code's resume hint on exit][claude-exit] as the model. The rules
below are airo's: a short, useful ending that leaves the answer prominent.

**Print it once when the command exits, after removing the live region.**
Completing a turn in an open conversation returns to the input. The agent's
answer stays on standard output; the CLI's closing message goes to standard
error. Do not add another summary of the answer, a success banner, or usage
statistics by default.
Leaving an empty conversation that created no session needs no closing hint.

| What happened | What the closing message needs |
| --- | --- |
| A single prompt finished, or the person left an idle conversation | Just the resume hint |
| The person detached from a running turn | Say the agent works on in the cluster, then give the resume command |
| The turn was canceled or failed | Keep the cancellation or failure reason visible; give the resume command if the session is known. Do not imply work continues |
| The connection failed before the turn's result arrived | Say the result is unknown and work may still be running; give the resume command if known |
| A prompt was sent but no session ID arrived | Say why there is no resume hint; do not invent an ID or fall back to `--continue`, which could select another session |

**Separate the closing message from the answer with a blank line.** Keep
any status to one short sentence. Then print `Resume this session with:`
on its own line, followed by the command on the next line. Use the Muted
role for the introductory line and the default foreground for the command,
with the same words and spacing in plain output. No border or decorative
separator is needed.

**Give one command that names this session.** Use the full agent name and
session ID, without truncation or placeholders. The ID appears once, in the
command; a separate `Session ID` line adds nothing. Put the command last so
it is easy to find and copy. Its line contains only the command: no
indentation, label, prompt mark, backticks or trailing explanation. Use this
format for session commands' resume hints too. An error already shown needs
no second copy.

An ordinary exit:

```text
The build fails in the step lint: main.go has an unused import.

Resume this session with:
airo agent -a reviewer -r 7c9e6679-7425-40de-944b-e07fc1f90ae7
```

Detaching from work that is still running:

```text
I am checking the build logs.

Detached. The agent works on in the cluster.
Resume this session with:
airo agent -a reviewer -r 7c9e6679-7425-40de-944b-e07fc1f90ae7
```

[codex-exit]: https://github.com/openai/codex/blob/main/codex-rs/tui/src/app/exit_summary.rs
[claude-exit]: https://github.com/anthropics/claude-code/blob/main/CHANGELOG.md#2131

## Cleanup

**A command removes what exists only for its task, and the cluster removes
it when the command cannot.**

| Resource | On exit | Backstop in the cluster |
| --- | --- | --- |
| Created for the task, such as the Tunnel of `airo tunnel 3000` | The command deletes it | The cluster deletes it soon after the command is gone, also when the process was killed |
| Outlives the terminal, such as an agent's session | Ctrl-C detaches, the work goes on | Its own system archives it after a longer time |
| Existed before the command | Left as it is | None |

**Never rely on the command's exit for cleanup.** A resource a command
creates for a task has an owner or an expiry in the cluster from the moment
it is created.

## APIs

**Resources go through the Kubernetes API.** What is not a resource, such as
an agent's sessions and chat, may use the component's own API on two
conditions:

- **The address comes from the cluster**, such as a resource's status or a
  ConfigMap in the project, not from a flag or a built-in URL. An address
  that is not `https` is refused, because the call carries the person's
  token.
- **The caller is the person signed in with `airo login`.** No self-declared
  identity.
