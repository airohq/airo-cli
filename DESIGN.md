# Design

How `airo`'s commands are shaped, for people and coding agents adding or
changing one. `airo` may be the only tool a person has, so it covers both
daily tasks and managing resources, without kubectl.

## Grammar

**A resource command is `airo <noun> [verb] [argument]`**, like `gh issue list`.
The noun is a kind of resource. The docs and the help write it in the
singular, and the plural works too.

```sh
airo tunnel 3000             # the noun's default task
airo tunnel list             # a verb
airo tunnel delete my-app
airo chat "fix the build"    # a prompt for the default agent
```

| Part | Rule |
| --- | --- |
| Noun | A kind of resource, such as `tunnel` or `agent`. `tunnels` and `agents` mean the same |
| No verb | The noun's default task, if it has one. Otherwise the help |
| `list`, `get`, `describe`, `edit`, `delete` | Work for every kind the cluster serves, also kinds this binary does not know |
| `create` | Resource-specific flags. Agent and tunnel also prompt for missing input on a terminal |
| Other verbs | Actions of one noun, such as `airo agent logs` |
| Inner noun | A part of a resource, such as `airo agent skill add SOURCE --agent NAME`. See below |
| No noun | Standalone actions such as `airo chat [PROMPT]`, `airo login`, `airo status`, `airo whoami`, `airo create -f`, `airo apply -f`, `airo version` |

**Every noun takes its plural.** `airo sessions list` is `airo session
list`, and `airo tunnels 3000` is `airo tunnel 3000`. A noun with its own
command names the plural as an alias, and a test fails when one does not. A
kind without a command takes the plural from the cluster. What is not a noun,
such as `airo login`, has one name.

**A noun that is not a kind in the cluster has only hand-written verbs.**
`airo session` is one: a session lives in the agents' API. It has `list`,
because people expect it, and no `get` or `delete`. Without a verb it prints
the help.

**A noun that is the platform's word for a kind has its own verbs too.**
`airo project` is one: a project is a Namespace, and `airo namespace list`
still works for the kind. Its `list` and `get` use the same Namespace table.

**A part of a resource that people change by itself is a noun inside the
resource's noun**: `airo agent skill add SOURCE`. Such a part, like a skill
in an Agent, is not a kind in the cluster.

- **The resource is a flag named for the outer noun**, with its short form:
  `--agent` or `-a`. It has no default, because the command changes the
  resource: without the flag, airo asks on a terminal, even when the project
  has one, and fails elsewhere. It is never a second argument, because
  everything but the one argument is a flag.
- **Its verbs are `list`, `add` and `remove`**, not `create` and `delete`,
  because they change the outer resource and make or delete none. A verb of
  its own may follow, such as `upgrade`, which moves a pinned version to a
  newer one, as `gh extension upgrade` and `brew upgrade` do.
- **It takes its plural** like any noun: `airo agent skills list`.
- **It changes only the part it names.** The resource's other fields stay,
  also those this airo does not know.

**A noun gets a short form when people already know one**, as with familiar kubectl resource names.

- **Take the short form people use elsewhere**, such as kubectl's short
  names and `kubectx`. Do not invent one.
- **A noun without a known short form has none.**
- **The README and the help use the long form**, and name the short one
  once.

**A noun's help groups its verbs as kubectl's help does.** The verbs every
kind has, `list`, `get`, `describe`, `edit` and `delete`, come first under
Basic Commands, also when the noun replaces one of them with its own, as
`airo project delete` does. The noun's own verbs follow in groups named
for what they are about, such as Agent Commands for `create`, `update` and
`skill`, and Local Run Commands for `serve` and `connect`. A group of one
verb is fine. A kind without a command of its own, such as `airo configmap
-h`, shows the same Basic Commands. A noun that is not a kind, such as
`airo session`, or an inner noun, such as `airo agent skill`, has no Basic
Commands and lists its verbs as one group. Add groups with
`ui.AddCommandGroup`, and the basic verbs with `generic.AddBasic`.

**`get` shows a resource's details, and `view` shows content.** `airo agent
get` prints the Agent as the cluster holds it. `airo session view` prints the
conversation.

## Default task

**A noun's default task is the one thing most people want from it**: `airo
tunnel 3000` serves a local port. A bare `airo agent` shows its resource commands.

- **Its argument is what the task acts on.** In `airo tunnel 3000 --name my-app`,
  the argument gives the port and the flag names the tunnel.
- **A noun whose task is to switch to one of its own is the exception.**
  `airo tenant acme` selects the tenant, and `airo project acme-web` selects
  the project. `airo project -` restores the previous project in that tenant.
- **A verb wins over an argument.** `airo tenant list` lists tenants.
  A tenant named `list` is selected with `airo tenant -- list`.
- **Free-form prompts belong to `airo chat`.** `airo chat list` sends the
  word `list`. `airo agent list` lists agents, and an unknown agent verb
  returns an error.
- **A missing choice is resolved in the cluster, then by asking.** With no
  `--agent`, `airo chat` takes the project's only agent, then the one marked
  as default with the annotation `airohq.dev/default-agent: "true"`, then
  shows a picker. Where nobody can answer, such as in a script, it stops and
  lists the names. The agent creation wizard shows the harness and applicable
  model choices, including a sole option. Its picker starts on the default
  marked by `airohq.dev/default-harness` or `airohq.dev/default-model`.
  Scripts use the only choice or the annotated default.
- **A default is an annotation on the resource, and the newest default
  wins.** Wherever a kind can be marked as the default, the mark is an
  annotation `airohq.dev/default-<noun>: "true"`, never a reserved name such
  as `default`. When several resources have it, the command takes the one
  created last, as Kubernetes does with the default StorageClass, and the
  name decides between two of the same second. It never stops because of
  two defaults. So a project's people override the platform's default by
  marking their own, and get it back by removing the mark. What the command
  creates refers to the resource by its real name, so a later change of the
  default moves nothing that exists.
- **Every picker is the same one**, from `internal/picker`. Choices appear inline
  below the question. Place the keyboard guide below the choices and any
  result count or empty-state message. Use Muted for this secondary help.
  Arrow keys move, typing filters, Enter chooses and Esc
  cancels. The selected name stays in the transcript. Plain terminals use
  numbered choices. Redirected commands take explicit arguments and flags.

## Arguments and flags

- **Everything but the one argument is a flag with a default that works**, so
  the shortest form does something useful.
- **A flag is named for what the person gets**, such as `--public` and
  `--no-auth`. The README names the field it sets.
- **Name the working scope with `--tenant` and `--project`.** A tenant
  selection belongs to one platform and sign-in. Each tenant remembers its
  project as the namespace of its kubeconfig context. Resource commands
  accept either override for one invocation. The cluster decides access.
- **Switch persistently with `airo tenant NAME` or `airo project NAME`.**
  Login stores explicit choices. On a first login, resolve a sole membership
  automatically and ask when several are available. Scripts pass `--tenant`.
- **Use `--name` when a flag names the command's own resource.** For example,
  `airo tenant --name phasi` selects the tenant named `phasi`.
- **When a flag refers to another resource by name, use the target resource's
  singular noun as the flag.** In `airo agent create --harness claude`,
  `--harness` selects the harness named `claude`. Use the same flag wherever
  another command refers to that resource.

## Capabilities and solutions

**A command behind a Tenant capability or solution says so before anything
else.** A tenant turns on features with the labels
`capabilities.airohq.dev/<name>` and `solutions.airohq.dev/<name>`, which
`airo tenant` lists. A command, or a flag such as `airo agent create
--local`, that only works with some of them names all of them:

- **The help starts with them.** The long help opens with
  `Required capabilities: agents, tunnels, local-agents`, then
  `Required solutions: adlc` when there are any, each list in the order the
  feature builds on them, followed by an empty line. A flag that adds a
  requirement names it in its own description, such as `(required
  capabilities: local-agents)`.
- **The README names them** in the command's section, in the same words.
- **The command checks them before it changes anything.** It reads the
  labels of the project's Tenant. When one is missing, it stops with an error
  that names the missing labels, the tenant and the project, and tells the
  person to ask a tenant administrator: `project acme-web does not have the
  local-agents capability of tenant acme: ask a tenant administrator to
  enable it`. A Tenant it cannot read is not a refusal: the command goes on
  and the cluster decides.
- **The cluster decides, the check only explains.** The capability is
  enforced by the grants and policies the platform gives the tenant, so
  kubectl and other clients meet the same limits. A refusal from the cluster
  that comes from a missing capability, such as Forbidden on a resource only
  that capability grants, gets the same error as the check. A refusal for the
  person's own permissions says that instead, with the verb and resource the
  cluster named.
- **A command that fails halfway removes what it created**, as in
  [Cleanup](#cleanup).

**Basic commands that work for every tenant say nothing about
capabilities.** `airo login`, `airo project`, `list`, `get` and the other
verbs every kind has need no line in the help and no check.

## Output

**Output starts with what the person needs next**, such as the URL.

**What a task produces goes to standard output, and notes about it to
standard error.** An agent's answer can be piped, and the session's ID does
not end up in it.

**An ID a command prints is one another command takes as it is.** The
session ID of `airo chat` and `airo session list` goes into `airo session
view`, `airo session cancel` and `airo chat -r`.

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

**Creation prompts start with the resource and current project.** Mark each
question with `?` in the chat prompt's Accent style. Ask only for missing
input and keep questions on standard error. A manifest supplied to
`airo create -f` goes directly through kubectl's generic creation path.

**A command stays in the scrollback.** No full-screen mode. A conversation
keeps a live region at the bottom, for the input and the status, and what
was said scrolls up as ordinary text. On exit the live region is gone, and
what the person needs next, such as how to resume, is the last thing
printed.

**Colours are named for what they mean, in one place.** `internal/ui` holds
them, and a command writes no escape code of its own.

**Keep the UI accent separate from Markdown.** Use Codex's quiet hierarchy
and adaptation to the terminal as the reference. Keep the current prompt
accent for small UI details: the `›` marker, focus and the current choice.
Do not carry that colour through every heading, link and code token. Markdown
has its own theme, described below. Prose and commands stay prominent through
readable text, spacing and weight. Use warm, muted amber for UI warnings.

| Role | Tone | Used for |
| --- | --- | --- |
| Default | The terminal's own foreground and background | Answer prose, prompts, commands and other ordinary text |
| Muted | A readable secondary neutral | Supporting IDs, hints, keys, timestamps and the agent's `•` marker |
| Accent | The existing prompt accent | The prompt's `›` marker, a focused control or the selected item's label |
| AgentName, HarnessName, ModelName | Lavender, soft green and warm yellow, respectively | The three bold values in the chat status line |
| Success | The terminal's green | A completed outcome, with `✓` or a word such as `Done` |
| Warning | Warm, muted amber | Something needing attention, with `!` and an explanation |
| Error | The terminal's red | A failure, with `✗` or `Error` and what went wrong |

**Adapt the shades to the terminal.** Codex's [semantic styles][codex-colours]
and [contrast handling][codex-contrast], pinned to CLI 0.169.0, are references
for the approach. The UI shades below retain the current prompt accent and
attention colour. They do not define the Markdown theme. Use them as starting
points on terminals that support RGB colours, then adjust for the actual
background:

| Tone | Dark background | Light background |
| --- | --- | --- |
| Accent | `#6BB8C4` | `#226D7A` |
| Warning | `#C6A46A` | `#826021` |
| AgentName | `#B4BEFE` | `#7287FD` |
| HarnessName | `#A6E3A1` | `#40A02B` |
| ModelName | `#F9E2AF` | `#DF8E1D` |
| Muted text | Blend about 60% of the terminal foreground with 40% of its background, then adjust for readability | The same rule, using the light theme's colours |

- **Inherit the canvas.** Do not impose a black background or white text.
  Optional input and selection fills should be subtle. For a neutral input
  fill, start with 12% white over a dark background or 4% black over a light
  one. Leave ordinary transcript text on the terminal's background.
- **Readability wins over an exact hex value.** When the background is known,
  aim for at least 4.5:1 contrast for informative text, including muted hints
  and coloured labels. Check the foreground against a selection's actual
  fill too. Faint decorative rules may have less contrast; essential text
  may not. Do not rely on the terminal's dim attribute to make text readable.
- **Fall back by capability.** RGB terminals can use the preferred tones;
  256-colour terminals use the nearest readable palette entry. On a terminal
  with only 16 colours, prefer its cyan for the accent when readable, and
  keep semantic green and red for outcomes. Use the default foreground with
  bold or underline wherever a palette colour lacks contrast. If the
  background cannot be determined, omit custom fills and favour the default
  foreground. Never assume ANSI colour 8 is readable grey on every theme,
  or yellow is readable on white. Colour detection must not delay startup
  or consume the person's input.
- **Use UI colour for UI meaning.** Keep the main accent on small controls.
  Colour a status marker or short label, leaving its explanation in Default.
  Markdown colours describe document structure and syntax; they do not reuse
  the UI's success, warning or error roles. Keep selection, warnings and
  failures distinguishable through words, markers or emphasis when colour is
  disabled.
- **Review both themes and fallbacks.** Preview dark and light terminals,
  16- and 256-colour output, `NO_COLOR`, `TERM=dumb`, and redirected output.
  A quiet appearance must still be easy to read.

[codex-colours]: https://github.com/openai/codex/blob/rust-v0.169.0/codex-rs/tui/src/style.rs
[codex-contrast]: https://github.com/openai/codex/blob/rust-v0.169.0/codex-rs/tui/src/style/contrast.rs

**Waiting is shown.** What takes longer than a moment shows a spinner on
standard error, with what it waits for and for how long. It leaves no trace
when it ends.

**A status line says what happens, never which keys to press.** Spinners,
progress, working and idle status lines carry the state, the subject and
the elapsed time. Keys that work everywhere, such as Ctrl-C to stop, are
not repeated there. Key help has three places only:

| Where | What |
| --- | --- |
| A picker's guide | One Muted line below its choices ([Default task](#default-task)), because the keys of a list are not obvious |
| A contextual hint | Once, after an action whose result needs explaining, such as the first Ctrl-C in a chat or Enter during a turn |
| Help | `/help` in a chat, and a command's `--help`. A chat on a plain terminal, which has no `/help`, says once as it starts how to leave |

A question may name its answer, such as `(Enter to skip)` or `(y/N)`,
since that tells what the answer means, not a key guide.

**An agent's text is rendered as markdown on a terminal, and is the agent's
own text in a pipe.**

**Markdown has its own theme.** Use Catppuccin Mocha on dark terminals and
Catppuccin Latte on light terminals, the default theme family used by Codex's
[Markdown renderer][codex-markdown] and [syntax highlighting][codex-syntax].
Follow [Catppuccin's markup scopes][catppuccin-markup] for document colours and
use the bundled Catppuccin syntax styles for code. Do not recolour the whole
theme with Airo's UI accent.

| Markdown element | Treatment |
| --- | --- |
| Headings | Rose for H1, peach for H2, yellow for the remaining levels; H1 is bold and underlined, H2 bold, H3 bold and italic, H4–H6 italic |
| Strong and emphasis | Rose, with bold and italic respectively |
| Inline code | Green, without the Markdown delimiter backticks |
| Links | Blue and underlined |
| Quotes and list markers | Pink quotes, teal bullet markers and blue numbered markers; ordinary list text stays neutral |
| Fenced code | The full syntax theme: distinct keyword, type, function, string and number colours, with readable muted comments |
| Tables | Bold yellow headers, a heavy horizontal header rule and light muted rules between body rows; no vertical borders |

Preserve these hues where readable, adjusting for the actual terminal
background and colour capability. Prose inherits the terminal foreground;
all content inherits its background, including diff tokens inside code.
Use the same theme for live answers and replayed history. Unknown terminal
colours fall back to readable neutral Markdown; pipes retain the original
Markdown source.

List markers include their punctuation in the marker colour. Preserve an
ordered list's starting number and align wrapped lines under its text.
Nest lists four columns beyond the parent's marker; keep blank lines in
loose lists and between paragraphs inside an item.

Give table cells one column of padding on each side and leave two columns
between cells. Size columns to their contents, wrap long prose, and respect
left, centre and right alignment. On narrow terminals, show labelled records
when columns cannot fit without splitting words. Keep inline formatting and
links inside cells; never truncate cell contents.

[codex-markdown]: https://github.com/openai/codex/blob/rust-v0.169.0/codex-rs/tui/src/markdown_render.rs
[codex-syntax]: https://github.com/openai/codex/blob/rust-v0.169.0/codex-rs/tui/src/render/highlight.rs
[catppuccin-markup]: https://github.com/catppuccin/bat/tree/main/themes

**Messages have space to breathe in the styled chat.** Leave one blank line
between messages, and between the conversation and the live input or status.
The person's message starts with bold `› ` in Accent, using the same
U+203A character as [Codex's prompt][codex-prompt]; the agent's starts with
`• ` in Muted. Use the same marker in the editable prompt. Reserve those two
columns on every continuation line, so wrapped prose and multiline prompts
align with the first character after the marker. Markdown lists, quotes,
tables and code keep their own
indentation inside that margin. Show a marker once per message, never once
per streamed chunk or paragraph. Live answers and restored history use the
same layout. Pipes keep the agent's original text without these markers.

```text
› Explain the failure and suggest
  what to try next.

• The build fails during lint.
  Remove the unused import and rerun it.

› Can you fix it?
```

**The agent's steps show between its messages, quieter than its words.** As
in Codex, a tool call is one Muted line on standard error with the `•`
marker: `Ran` and the command for a shell, or `Used` and the tool's name
with its main argument. A result that worked adds nothing; a failed one is a
line with `✗` in Error. A tool call ends the message before it, and the text
after it is a new message with its own marker. The agents' API marks only
tool activity: it does not tell a preamble from the answer, nor carry the
model's reasoning, so `airo` shows neither as such.

[codex-prompt]: https://github.com/openai/codex/blob/rust-v0.169.0/codex-rs/tui/src/bottom_pane/chat_composer.rs#L4895

**Resuming an interactive conversation restores what was said.** Both
`--resume` and `--continue` load saved prompts and answers, oldest first,
before accepting the next prompt. Use the same formatting as live chat,
and include earlier prompts in Up/Down history. Keep a draft typed during
loading, without sending it automatically. Plain terminal conversations
replay history too. A single prompt, supplied as an argument or on stdin,
prints only its new answer. Loading history is a snapshot, not a connection
to an already-running turn's stream; label saved unfinished states clearly.

**The status line identifies the conversation.** Under the input, show the
agent's name, configured harness and model on the left, without `Agent`,
`Harness` or `Model` prefixes. Separate them with muted middle dots. Use
bold lavender for the agent, soft green for the harness and warm yellow for
the model, taking the tone from Codex's status line. Keep the session ID
muted. Adapt these colours to the background and fall back to neutral bold
when colours are unknown or limited. Resolve the model's actual name from
its ModelConfig, including referenced templates;
do not present a configuration alias as the model. Load these details without
blocking input, and mark unavailable fields as unknown. These are the Agent's
current settings, not a claim about a resumed session's pinned revision.
On the right, show the full session ID as soon as it is known, including
immediately on resume. Keep it easy to select and copy. If the row is too
wide, move the ID to a separate right-aligned line; never abbreviate it.
Keep the working status and elapsed time above the input while a turn runs.

**Keys mean what people know from other tools.** Keep routine keyboard help
behind `/help`, with contextual hints after an action needs explanation,
such as Enter during a turn or the first Ctrl-C. Do not repeat the key guide
in the idle or working status line, or in the wait before a chat starts
([a status line says what happens](#terminal)).

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
`airo chat /help` sends the literal text. A slash command never adds a
second way to manage platform resources; those remain CLI commands.

**A resource looks as kubectl prints it.** `list` and `get` without a name
show the collection. `get NAME` shows one object. Both verbs keep the
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
airo chat -a reviewer -r 7c9e6679-7425-40de-944b-e07fc1f90ae7
```

Detaching from work that is still running:

```text
I am checking the build logs.

Detached. The agent works on in the cluster.
Resume this session with:
airo chat -a reviewer -r 7c9e6679-7425-40de-944b-e07fc1f90ae7
```

[codex-exit]: https://github.com/openai/codex/blob/main/codex-rs/tui/src/app/exit_summary.rs
[claude-exit]: https://github.com/anthropics/claude-code/blob/main/CHANGELOG.md#2131

## Cleanup

**A command removes what exists only for its task, and the cluster removes
it when the command cannot.**

| Resource | On exit | Cleanup after interruption |
| --- | --- | --- |
| Durable Agent created by `airo agent create --local` | Ctrl-C in `airo agent connect NAME` disconnects the machine. The Agent stays available for reconnection | Deleting the Agent garbage-collects its dedicated relay resources |
| Temporary Agent created by `airo agent serve`, including with `--name` | The command deletes the Agent | The janitor deletes the expired Agent. Kubernetes garbage-collects its dedicated relay resources whenever the Agent is deleted |
| Codex or Claude subscription login | The container sandbox prepares credentials before `serve` creates an Agent or `connect` starts serving. The engine keeps them for later workers. | The user can remove the provider’s credential volume printed at sign-in |
| Host directory shared with `--workspace` | Edits remain on the host | Owned by the user |
| Temporary volume of `airo agent serve` | Remove the volume after the supervisor stops | The next serving process in the same project and engine can reclaim an abandoned volume after its Agent disappears |
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

**Public services outside the platform may have a built-in address** when
the call carries no credentials and holds nothing of the project. Pinning a
skill reads its Git host or registry, and `airo agent skill search` asks a
public directory of skills such as skills.sh. Such a call sends only what
the person typed, such as a source or search words, and an answer from it
is data: text shown on the terminal is stripped of control characters.
