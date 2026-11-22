# airo

> [!CAUTION]
> **Early alpha.** Commands and flags may change.

`airo` is the command-line tool of the Airo platform: sign in, switch between sign-ins and projects, talk to an
agent, see your sessions, manage the resources of your project, and give a local port a URL.
Each command reads and writes Kubernetes resources as you, so everything it
does to a resource can also be done with `kubectl`, and you do not need
`kubectl` to use it.

Run `airo -h` for commands grouped by task and `airo options` for global
flags such as `--context` and `--namespace`. Each command has its own help,
for example `airo login -h` for first-time setup. Once signed in, run
`airo project list` to find your projects and `airo agent list` to see the
agents in your current project.

## Install

**Install and update the stable release with Homebrew, on macOS and Linux.**

```sh
brew install airohq/tap/airo
brew upgrade airo        # update
airo --version
```

Homebrew installs shell completions for zsh, Bash, fish and PowerShell.
In a new shell with completion enabled, type `airo ` and press Tab for
commands, `airo agent ` and Tab for agent verbs, or `airo --` and Tab for
flags. For shell setup, see [Homebrew's completion guide](https://docs.brew.sh/Shell-Completion).

The binaries are also on the [releases](https://github.com/airohq/airo-cli/releases) page.

**To follow `main`, install with Go.** Run the same command again to update.

```sh
go install github.com/airohq/airo-cli/cmd/airo@main
```

## Sign in

**`airo login` signs you in through the browser.** The first time, pass where
the platform is. The platform's admin gives you the four values, and they
stay in your kubeconfig.

```sh
# the first time
airo login --server https://api.example.com:6443 --certificate-authority ca.crt \
  --issuer https://login.example.com/ --client-id abc123

airo login                       # sign in again, for example after a new role
```

```text
Open https://login.example.com/activate?user_code=ABCD-EFGH
and confirm the code ABCD-EFGH
Signed in as auth0:abc123
  context:   airo
  namespace: acme-default
  groups:    customer:acme
```

**It adds the platform to your kubeconfig as the current context**, so kubectl
works with the same sign-in when you have it. The kubeconfig holds no
credentials. `airo` keeps the tokens in your cache directory and refreshes
them, and the kubeconfig runs `airo credential` to read them, so `airo` must
be on your `PATH`.

| Sign-in | What happens |
| --- | --- |
| First, with the platform's flags | Writes a cluster, a user and the context `airo` into your kubeconfig. It works without a kubeconfig, and other entries stay as they are |
| A customer's first | Sets the context's namespace to the project `<tenant>-default`. A namespace you chose stays |
| Again, without flags | Signs in again in the current context. When that is not the platform's, such as a local cluster's, it uses the context `airo` |
| With `--context NAME` | Signs in to that context. One that does not exist yet is created for the same platform, with its own tokens |
| With a platform flag again | Replaces that value in the context, for example after the issuer changed |
| With `-n NAMESPACE` | Makes that project the context's namespace, for every later command, as `airo project NAMESPACE` does without a new sign-in |
| In no customer group, such as a platform admin | Sets no namespace, so commands use `default` until you pass `-n` or choose one with `airo project` |

| Flag | The first sign-in's value |
| --- | --- |
| `--server` | The API server's address |
| `--certificate-authority` | The file with the API server's certificate authority. `--certificate-authority-data` takes it as base64 instead. Neither is needed when the certificate is publicly trusted |
| `--issuer` | The OIDC issuer's URL |
| `--client-id` | The OIDC client ID |

**You can be signed in as two people at once, each in its own context.**
Tokens are kept per context name.

```sh
airo login --context customer       # sign in as someone else: a new context
airo agent list --context airo      # one command as the first person
airo login --context airo           # sign in as the first person again
airo context customer               # switch without signing in again
```

The browser remembers who signed in last. To sign in as someone else, open
the printed URL in a private window.

`airo login` never changes a context that does not sign in through the
platform. It writes to `~/.kube/config`, or to the file in `KUBECONFIG` or
`--kubeconfig`.

`airo` takes kubectl's flags, such as `-n`, `--context` and `--kubeconfig`,
and works in the context's namespace by default.

## Context

**`airo context` switches between your sign-ins**, for every later command of
`airo` and `kubectl`. `airo ctx` is the short form, and `airo contexts` also
works.

```sh
airo context             # choose from a picker
airo context customer    # switch to the context customer
airo context -           # switch back to the previous one
airo context list
```

```text
Switched to context customer
  namespace: acme-web
```

| Command | What happens |
| --- | --- |
| `airo context` | Shows an inline list with the current context selected. Use the arrow keys or type to filter, Enter to choose, and Esc to cancel. Plain terminals show numbered choices. Where nobody can answer, such as in a script, it stops and lists the names |
| `airo context NAME` | Makes that context the kubeconfig's current one |
| `airo context -` | Switches to the context you last switched away from, so twice brings you back. `airo` keeps its name in your cache directory |
| `airo context list` | Lists the sign-ins with their namespaces, and marks the current one with `*` |
| `airo context delete NAME` | Removes the named sign-in context from the kubeconfig. If it was active, clears its selection. The cluster and user entries stay available to other contexts |
| `airo context set [NAME]` | Changes what a sign-in holds, without signing in again |

**`airo context set` changes a sign-in's values in the kubeconfig**, such as
the platform's address after it moved. It is `airo`'s form of `kubectl config
set-context` and `set-cluster`. Without a name it changes the current
context, and only the flags you pass are changed.

```sh
airo context set --server https://api.example.com
airo context set -n acme-web                 # work in another project from now on
airo context set customer --certificate-authority ca.crt
```

```text
Set context airo
  server:    https://api.example.com
```

| Flag | Sets |
| --- | --- |
| `--server` | The cluster's `server`: the API server's address |
| `--certificate-authority`, `--certificate-authority-data` | The cluster's `certificate-authority-data` |
| `--issuer`, `--client-id` | The arguments of the user's exec plugin. The next command signs you in again |
| `-n` | The context's `namespace` |

Sign-ins at the same platform share the cluster's entry, so a new address or
certificate authority applies to all of them, and the command names them. It
does not make a sign-in: `airo login` does.

**It knows only the contexts that sign in through the platform.** Others,
such as a local cluster's, are not listed and not switched to. Use `kubectl
config use-context` for them.

A context named like a verb goes after `--`, as in `airo context -- list`.

## Project

**`airo project` switches the project your commands work in**, for every
later command of `airo` and `kubectl`. A project is a namespace of the
platform, and the switch sets the namespace of your kubeconfig's context.

```sh
airo project             # choose from a picker
airo project acme-web    # switch to the project acme-web
airo project -           # switch back to the previous one
airo project list
airo project create acme-web
airo project delete acme-web
```

```text
NAME           STATUS   AGE
acme-default   Active   9d
acme-web       Active   2d
```

| Command | What happens |
| --- | --- |
| `airo project` | Shows an inline list with the current project selected. Use the arrow keys or type to filter, Enter to choose, and Esc to cancel. Plain terminals show numbered choices. Where nobody can answer, such as in a script, it stops and lists the names |
| `airo project NAME` | Makes that project the context's namespace. The name must be one of your projects |
| `airo project -` | Switches to the project you last switched away from in this context, so twice brings you back. `airo` keeps its name in your cache directory |
| `airo project list` | Lists accessible Namespaces with the cluster's columns. Use `airo status` to see the current project |
| `airo project create NAME` | Creates the project, and returns when the platform lists it, so `airo project NAME` takes it. It does not switch to it |
| `airo project delete NAME` | Deletes the project and everything in it. If your commands work in it, they switch to the project you were in before, as `airo project -`, or else to your default project, as `airo login` chooses it |

**It changes the current context, or the one `--context` names**, and only a
context that signs in through the platform. For one command in another
project, pass `-n` to that command instead. `airo context set -n NAME` also
sets the project, without asking the cluster whether it is one of yours.

**Your projects are the namespaces the platform's API lists for you.** A
customer's people see their own projects. A platform admin sees every
namespace of the cluster.

**The platform decides which projects you may create and delete.** A
customer's name must start with the tenant's name and a dash, such as
`acme-`, and a tenant has a limited number of projects. The default project,
such as `acme-default`, cannot be deleted. When the platform refuses, `airo`
prints its reason.

A project named like a verb goes after `--`, as in `airo project -- list`.

## Status and identity

**`airo status` shows where you are working and checks your connection.**

```sh
airo status
airo status --context customer    # inspect another sign-in
airo status -n acme-web           # inspect another project
```

```text
User:        auth0:abc123
Tenant:      acme
Project:     acme-default
Context:     airo
Server:      https://api.example.com:6443
Connection:  Connected
```

Project is currently the Kubernetes namespace. `--context`, `-n`,
`--server`, and `--kubeconfig` select the values for this invocation without
changing the saved context. An unset project uses `default`, as other
commands do. Connected means the Kubernetes API answered the identity
check; it does not check access to the project or the health of its resources.

If the identity check fails, status still prints the selected project,
context, and server, with `unknown` for user and tenant and `Unverified` for
the connection. It reports the error on standard error and exits nonzero.

**`airo whoami` shows your user, tenant, and groups**, like `kubectl auth
whoami` with tenant membership included.

```sh
airo whoami
airo whoami --context customer
```

```text
User:    auth0:abc123
Tenant:  acme
Groups:  customer:acme, system:authenticated
```

Both commands ask the Kubernetes API for your identity using a
`SelfSubjectReview`, with the credentials and impersonation flags of this
invocation. Tenant names come from the returned `customer:<tenant>` groups.
All tenant memberships are shown when there is more than one; `none` means
there is no customer group, such as for a platform admin. Groups are shown
as returned by the API, or `none` when empty. An identity check that fails
makes either command exit nonzero.

## Tenant details

`airo tenant` lists your tenant's solutions and capabilities in separate tables.
The command uses the tenant membership shown by `airo whoami`.
If you belong to several tenants, choose one with `airo tenant --name NAME`.
Platform admins can use the same flag to inspect a tenant by name.

```console
$ airo tenant
Tenant: acme

SOLUTION  STATE
adlc      enabled

CAPABILITY  STATE
agents      enabled
tunnels     disabled
```

Each table lists its Tenant labels in alphabetical order. Solutions use
`solutions.airohq.dev/<name>` and capabilities use `capabilities.airohq.dev/<name>`.
A value of `"true"` shows as enabled. Any other value shows as disabled.

## Resources

**Every kind of resource in the cluster is a noun with `list`, `get`, `describe` and
`delete`**, including custom resources added after this CLI was built.
`airo RESOURCE list` and `airo RESOURCE get` both run the equivalent of
`kubectl get RESOURCE`. Add a name to `get` to show one object. Both verbs
use kubectl's Go library for discovery, server-provided columns, pagination,
and output formatting, using your kubeconfig credentials and permissions.

`airo RESOURCE describe NAME` uses the same describers as `kubectl describe`.
For custom resources it renders the fields returned by the API. An exact name
match includes related events when available. Use `--show-events=false` to
omit them. If the exact name is missing, kubectl tries it as a name prefix.

`airo RESOURCE delete NAME` uses kubectl to delete one named object and
waits for deletion by default. Use `--wait=false` to return when the API
accepts the request, or `--dry-run=server` to have the API check it without
deleting the object. A project dry run also preserves the selected project.

```sh
airo agent list                    # in your project
airo agent list -A                 # in every namespace you may read
airo agent get my-agent            # one row
airo agent get my-agent -o yaml    # the whole resource, or -o json
airo agent describe my-agent       # details and related events
airo pods get -o wide              # include the API's additional columns
airo agent delete my-agent
airo create -f agent.yaml          # create once, including custom resource kinds
airo apply -f agent.yaml           # create or update from a file, a directory, a URL or -
```

`airo create -f FILE` uses kubectl to create the resources in a YAML or JSON
manifest. An existing object returns an AlreadyExists error. Use
`airo create -f -` to read standard input or add `--dry-run=server -o yaml`
to see what the API would accept.

**A noun works in the singular and the plural.** `airo agents list` is `airo
agent list`, and the same holds for `airo sessions` and `airo tunnels`.

**A kind takes the names kubectl takes**: `agent`, `agents`, a short name, or
`agents.api.kagent.dev` when two groups have the same kind or a resource
shares a name with an Airo command. `airo apply` is a
server-side apply with the field manager `airo`.

## Chat

**`airo chat` sends a prompt to an agent of your project and prints the
answer as it arrives.** The prompt starts a new session. If you used
`airo agent [PROMPT]` in an earlier release, use `airo chat [PROMPT]` now.

```sh
airo chat "Why does the build fail?"    # a new session
airo chat -c "Fix it."                  # the next prompt of the same session
airo chat                               # a conversation, on a terminal
```

```text
The build fails in the step lint: main.go has an unused import.

Resume this session with:
airo chat -a reviewer -r 7c9e6679-7425-40de-944b-e07fc1f90ae7
```

| Argument or flag | Default | Meaning |
| --- | --- | --- |
| `PROMPT` | a conversation | What to ask the agent. Without it, Enter sends a prompt of one session, until Ctrl-D on an empty prompt. Input that is not a terminal, such as a pipe, is one prompt |
| `-a`, `--agent` | see below | The Agent's name |
| `-c`, `--continue` | off | Send the prompt to your last session with the agent |
| `-r`, `--resume` | none | Send the prompt to the session with this ID |

**Without `--agent`, the cluster decides which agent answers.** `airo` takes
the first that applies:

1. The project's only Agent.
2. The Agent with the annotation `airohq.dev/default-agent: "true"`. When
   several have it, the one created last: mark your own agent to use it in
   place of the platform's `assistant`.
3. The one you choose inline on a terminal. Use the arrows or type to filter
   and press Enter. Plain terminals show numbered choices. Elsewhere the command stops and lists the names.

**`airo` waits until the agent can take a session, and says why it waits.**
It reads the Agent's `Ready` condition, such as in a new project, where the
agent's first snapshot takes a few minutes. When the project's endpoint declares a
credential readiness ConfigMap, `airo` also waits for it before sending the prompt. On a terminal a spinner shows
the reason and the time, and Ctrl-C stops the wait. Elsewhere each reason
is a line on standard error. An agent the cluster has turned down, such as
for a model its Harness cannot use, stops the command with the reason.

Once connected, chat watches the selected Agent and checks it before each
prompt. If the Agent is deleted, replaced under the same name, or loses
readiness, chat closes and reports the reason, including while a turn is
streaming.

```text
Waiting for agent assistant to become ready: waiting for the ActorTemplate golden snapshot
Agent assistant is ready.
```

**The first prompt of a session starts its sandbox in the cluster**, and so
does the first one after `--continue` or `--resume`, when the sandbox has
paused. The terminal chat's status line says `Waking up the agent` until
the answer starts. The agents' API does not tell a starting sandbox from a
thinking model, so later prompts show the agent's state instead.

**A session stays in the cluster after the command ends.** Nothing is
deleted. `--resume` takes the session's ID, which every run prints and
[`airo session list`](#session) shows.
`--continue` takes the last session this machine started or resumed with
the agent. `airo` remembers one ID for each agent in `airo/sessions.json` in
your cache directory.

With no prompt argument, `airo chat -r SESSION` and `airo chat -c` load the
saved conversation before taking your next prompt. Earlier answers render
as Markdown in the terminal chat, and Up and Down recall earlier prompts
too. Plain terminal conversations show the same history without styling.
History is a snapshot; resuming does not follow new output from a turn that
is already running. With an argument or piped input, only the new answer is
printed; use `airo session view SESSION` to read the saved conversation.

**On a terminal, the chat stays in your scrollback.** Answers render as
Markdown above the input, with a status and elapsed time while the agent
works. It uses your terminal's colours and never takes over the full screen.
Under the input, the status line shows the agent name, configured harness
and model without prefixes, separated by muted dots: lavender for the agent,
soft green for the harness and warm yellow for the model. The full session
ID stays muted on the right for copying. On narrower
terminals the ID moves to its own line. The model name comes from the
referenced ModelConfig; unreadable details show as unknown without blocking
the conversation. These are the Agent's current settings; resumed sessions
can still use an older revision. Routine keyboard guidance appears through
`/help`, while contextual hints explain actions such as interrupting a turn.
The prompt mark keeps its UI accent; hints and the agent's marker use a
readable neutral. These adapt to the terminal's reported
background and colour support, with separate tones for light and dark themes.
RGB and 256-colour terminals get shades adjusted for readable contrast;
limited or unreported colours fall back to the default foreground and
emphasis. Warning labels use muted amber when supported; success and error
labels keep the terminal's green and red. The UI accent stays on small
controls such as the prompt marker.

**Markdown has its own colours.** It uses Catppuccin Mocha for dark terminals
and Latte for light terminals, the same default theme family as Codex.
Headings use rose, peach and yellow; inline code is green without delimiter
backticks, links are blue,
and quotes are pink. Fenced code uses the full syntax theme, with distinct
colours for keywords, functions, strings and numbers. Comments remain
readable. Prose and typed text keep the terminal's foreground, and all
content inherits its background. If the terminal cannot report its colours,
Markdown uses neutral emphasis. Theme detection runs alongside the chat
without delaying input or sending a draft.

Numbered list markers are blue and bullet markers are teal. Wrapped lines
align under the item text, with nested lists indented four columns. Lists
keep their starting number, checkboxes and paragraph breaks.

Tables use bold yellow headers and muted horizontal rules, with open space
between columns. They preserve cell alignment and inline formatting, wrap
long prose, and switch to labelled records when columns cannot fit legibly.

In the styled chat, a blank line separates messages. Your messages start
with bold `› `, also used in the editable prompt; the agent's start with `• `.
Continuation lines align two columns in, including wrapped prompts and
Markdown lists or code blocks. Each marker appears once per message, also
while an answer streams or history reloads.
The closing hint is separated from the answer by a blank line.
`Resume this session with:` uses the chat's muted tone when available; the
command follows on its own line, with no indentation or prefix, ready to copy.
Both a single prompt and a conversation end with one resume command
carrying the session ID, without a separate `Session` line. The hint appears
when the command exits, not after every turn.

| Key | What it does in the terminal chat |
| --- | --- |
| Enter | Sends the prompt |
| Shift-Enter or Ctrl-J | Adds a new line; use Ctrl-J when your terminal cannot distinguish Shift-Enter |
| Up, Down | Recalls sent prompts; Down past the newest restores your draft |
| Esc | Asks the agent to stop the current turn; the session stays open |
| Ctrl-C | Clears an idle draft; on an empty prompt or during a turn, explains how to leave |
| Ctrl-C twice | Leaves; a running turn continues in the cluster |
| Ctrl-D | Leaves when the prompt is empty |

**You can draft the next prompt while the agent works.** Enter keeps that
draft until the turn ends; press Enter again when ready to send it. Nothing
is sent automatically. `/help` shows the chat's help, and `/exit` leaves.
Other slash-prefixed prompts go to the agent unchanged. These two shortcuts
apply only in the chat input: `airo chat /help` sends `/help` to the agent.

**Leaving a running turn detaches, and the agent works on in the cluster.**
The command prints how to come back and exits without an error:

```text
Detached. The agent works on in the cluster.
Resume this session with:
airo chat -a reviewer -r 7c9e6679-7425-40de-944b-e07fc1f90ae7
```

**The answer goes to standard output and everything else to standard
error**, so the answer can be piped. The command fails when the agent fails,
turns the prompt down or the turn is canceled. In a conversation it prints
the failure and takes the next prompt.

**Pipes keep the agent's original text.** The terminal chat requires stdin,
stdout and stderr to be terminals. `NO_COLOR` (even empty), `TERM=dumb`, or
redirected stderr selects the plain conversation, with one line per prompt
and no styling or redraws. In plain mode one Ctrl-C detaches. Redirected
stdout disables prompts and pickers: pass the prompt as an argument or on
stdin. For example, `airo chat "Explain the build" > answer.md` writes the
agent's Markdown directly to the file.

**The command talks to the agents' API as you**, with the
[A2A](https://a2a-protocol.org) protocol 1.0. A session is not a resource,
so this command does not go through the Kubernetes API alone:

| What | Where it comes from |
| --- | --- |
| The address | The key `url` of the ConfigMap `kagent` in your project. It must be `https` |
| The agent's endpoint | `<url>/agents/<namespace>/<agent>` |
| The caller | Your sign-in: the token `airo login` got, read again for every prompt |

A project without that ConfigMap has no agent endpoint. Ask a platform
admin.

## Agent

Run `airo agent` or `airo agents` to see the resource commands. To inspect an
agent, use `airo agent get NAME`. To talk to it, use `airo chat -a NAME`.

**`airo agent create` makes an agent from a prompt and waits until it is
ready.**

```sh
airo agent create                  # choose a name and instructions or template
airo agent create reviewer --prompt "You review pull requests."
airo agent create reviewer --prompt-file prompt.md --model opus-5.5 --harness claude
airo agent list
airo agent delete reviewer
```

```text
Created agent my-project/reviewer
  harness:  claude (claude)
  model:    sonnet-5.5
  ready
```

| Argument or flag | Default | Meaning |
| --- | --- | --- |
| `NAME` | prompted on a terminal | The Agent's name. Required in scripts |
| `--prompt` | none | The system prompt (`spec.template.inline.systemPrompt`) |
| `--prompt-file` | none | A file with the system prompt, or `-` for standard input |
| `--description` | none | What the agent is for, in a line (`spec.template.inline.description`) |
| `--model` | see below | The ModelConfig to use (`spec.template.inline.modelConfig.name`) |
| `--harness` | see below | The Harness that runs the agent (`spec.harnessRef.name`) |
| `--local` | off | Create a durable Agent and its dedicated private relay in the project |
| `--template` | none | An AgentTemplate to use in place of a prompt, a description and a model (`spec.template.ref.name`) |

**The Harness, the ModelConfig and the AgentTemplate exist in the project
already.** The platform puts Harnesses and ModelConfigs there, and `airo`
creates neither. The wizard lists harnesses, models and templates directly
below their questions. A muted keyboard guide appears beneath the choices.
Use ↑/↓ or type to filter, then press Enter. The
selected name stays beside the question as the wizard continues. Plain
terminals show numbered choices. A project default appears first and is marked `(default)`.
Passing `--harness` or `--model` supplies that choice directly.

A template supplies the agent's model configuration. A harness that runs
its own image (`byo`) lets you skip the model or choose one.

In scripts, supply a name and one of `--prompt`, `--prompt-file` or
`--template`. An omitted harness or required model uses the project's only
choice or its annotated default (`airohq.dev/default-harness: "true"` or
`airohq.dev/default-model: "true"`). The newest default wins. If the choice
is ambiguous, the command lists the names and the flag to pass.

**The Agent has only the fields the flags set:**

```sh
kubectl apply -f - <<EOF
apiVersion: api.kagent.dev/v1alpha3
kind: Agent
metadata:
  name: reviewer
spec:
  harnessRef:
    name: claude
  template:
    inline:
      systemPrompt: You review pull requests.
      modelConfig:
        name: sonnet-5.5
EOF
```

**An agent the cluster turns down stays in the project.** The command stops
with the cluster's reason, such as a model its Harness cannot use. Delete the
agent, or change it with `airo apply -f`. Tools, skills and the other fields
of an Agent go in a file for `airo apply -f` too.

### Serve an agent on your machine

Run `airo agent serve` to create a temporary Agent and private Tunnel in a project
with a [temporary relay configured](docs/remote-agent-demo.md#temporary-agents).
Use `--name` to choose the temporary Agent's name, or omit it to generate one.
The server prints `Created temporary agent NAME`. Use that name with
`airo chat -a NAME` in another terminal in the same project. An existing
Agent with that name causes creation to fail.

```sh
airo agent serve --name demo
```

Ctrl-C deletes the temporary Agent. Kubernetes then deletes its Tunnel.
Running `airo agent delete NAME` triggers the same cascade. While serving,
the CLI renews the Agent's `airohq.dev/expires` annotation every five minutes.
If the process dies, the janitor can delete the Agent 15 minutes after the
last renewal. Automatic Agent expiry requires the platform janitor to monitor
`agents.api.kagent.dev`. Each relay serves one temporary Agent at a time. Each `serve` invocation
gets a fresh local workspace on its first turn, even when you use the same
`--name`. Files and coding-agent history survive idle worker restarts during
that invocation. Stopping `serve` removes its workspace volume. After a hard
kill, a later first turn on the same relay and engine can reclaim the volume
once its Agent is gone. Provider credentials can be reused across invocations.

### Connect this machine to a durable Agent

For a durable Agent, use `--local` in a project with agents and tunnels enabled. The CLI
creates the Agent first, then its same-name private Tunnel, EgressPolicy and
Harness, and the Role and RoleBinding for project sessions to call this Tunnel.
Each child has the Agent as its sole owner. The Role is named after the Agent UID
so its binding can grant only that Tunnel's `call` permission. The relay uses
the worker pool and snapshot location of the project's default or only Harness.

```sh
airo agent create reviewer --local --prompt "You review pull requests."
airo agent connect reviewer
```

Deleting the Agent with `airo agent delete reviewer` removes its relay
through Kubernetes garbage collection. If creation fails partway, the CLI
deletes the Agent it created. After a forced kill during setup, deleting the
Agent also cleans up its children. For a custom shared relay, use
an Agent manifest with explicit Harness and EgressPolicy references.

Use `airo chat -a reviewer` in a second terminal. Ctrl-C in the serving
terminal disconnects the machine. Run `airo agent connect reviewer` again to reconnect.
The Agent and Tunnel persist.

### Local sandbox settings

Both `serve` and `connect` default to read-only Codex when you omit the coding-agent settings.
The first turn starts a container, later turns reuse it, and five idle
minutes release it. Temporary Agents keep their files until `serve` exits.
Durable Agents keep theirs across `connect` invocations on the same engine.

Choose the coding agent with `--harness claude` or `--harness codex`.
`--sandbox` selects where it runs. The default and currently supported
sandbox is `container`. The original `--runner` spelling also works.
`--codex-sandbox` controls Codex's own filesystem access inside that
environment.

The `runner` Dockerfile target in `kagent-remote-harness` builds a single
worker image containing both Claude Code and Codex. That repository's
GitHub Actions publish it to `ghcr.io/airohq/kagent-remote-harness/runner`.
On the first turn of each `serve` or `connect` invocation, the embedded container
sandbox pulls the default `ghcr.io/airohq/kagent-remote-harness/runner:latest`,
even when the tag is cached. It uses the resolved image ID for the rest of
that invocation, including after idle timeouts. If the pull fails, the turn
reports the error and the next turn retries. Other `--image` values use a
worker image already present in the selected engine.
To test a development branch, build from the matching harness checkout:

```sh
docker build --target runner -t local/remote-harness:dev .
airo agent connect reviewer --harness codex --sandbox container --codex-sandbox workspace-write --image local/remote-harness:dev
```

To use your ChatGPT subscription, sign in once from the serving machine:

```sh
airo agent login codex
```

Open the URL printed by Codex and enter the device code. The default Codex
worker for `serve` and `connect` can then use and refresh that login. Its
credentials live in a separate volume on the selected container engine.
The worker can read those credentials. Signing in uses the published image
pinned by the CLI and also works before selecting an Airo project. To forget
the login locally, stop the workers and run `docker volume rm VOLUME` with
the credential volume name printed by the sign-in command.

An exported `CODEX_API_KEY` or `OPENAI_API_KEY` takes precedence. Unset both
in the serving shell to use the subscription. Custom worker images and
external supervisors use their own credentials. For a Claude subscription,
export `CLAUDE_CODE_OAUTH_TOKEN` from `claude setup-token` in the serving
shell. An Anthropic API key can also be supplied as `ANTHROPIC_API_KEY`.
Add `-v` or `--verbose` to see the image, credential variable names, turn
event types and recognized failure reasons on stderr. To select another Engine API socket,
set `DOCKER_HOST`.

To run a separate supervisor, pass `--runner-command /path/to/remote-harness`.
Flags after `--` are passed through to that program. The embedded runner
also accepts the previous `-- --harness ...` syntax.

The project needs an Agent with a remote Harness whose `spec.env` sets
`REMOTE_HARNESS_URL` to a private Tunnel's `status.callerURL`. The Agent's
EgressPolicy must inject its Substrate actor JWT into `Authorization` for
that origin, with the same URL as audience. Grant the actor `call` on that
Tunnel and the person serving it `connect`. The command reads the Agent,
Harness and Tunnel and opens the outbound connection. A single machine
should serve each runner Tunnel. A project-wide actor `call` grant lets
other authorized sessions in that project run turns here.

Ctrl-C stops serving and cancels an active local turn. For a durable Agent, reconnect with the
same Tunnel and engine to reuse the workspace. See the
[remote harness](https://github.com/airohq/kagent-remote-harness) for the
runner settings and execution boundary.

## Session

**`airo session` shows your sessions with the agents of your project.** A
session is a conversation with one agent. `airo chat` starts one and prints
its ID.

```sh
airo session list                # your sessions in the project, newest first
airo session list -a reviewer    # with one agent
airo session view SESSION        # the conversation
airo session cancel SESSION      # stop the turn that runs
```

```text
SESSION                                AGENT      LAST ACTIVE   STATE     FIRST PROMPT
7c9e6679-7425-40de-944b-e07fc1f90ae7   reviewer   5s            working   Why does the build fail?
0b0f8f0e-1c0f-4a43-9c58-3f2f1d0a7a11   coder      3h            done      Add a test for the parser.
```

| Command | What it does |
| --- | --- |
| `airo session list` | Lists your sessions. `LAST ACTIVE` is the time since the newest turn changed, and `STATE` is that turn's state |
| `airo session view SESSION` | Prints each prompt and the agent's answer, oldest first, with who wrote it and when, and then the session's state |
| `airo session cancel SESSION` | Stops the turn that is not over and prints what it canceled. The session stays and takes the next prompt. With no such turn it says so and succeeds |

| Flag | Default | Meaning |
| --- | --- | --- |
| `-a`, `--agent` | every agent | The Agent's name. Only that agent is asked, which is faster |
| `-o`, `--output` | a table or text | `json` or `yaml`, on `list` and `view` |

```text
Session 7c9e6679-7425-40de-944b-e07fc1f90ae7 with agent reviewer

You, 2026-11-17 09:20
  Why does the build fail?

Agent, 2026-11-17 09:20
  The build fails in the step lint: main.go has an unused import.

State: done, last active 25m ago

Resume this session with:
airo chat -a reviewer -r 7c9e6679-7425-40de-944b-e07fc1f90ae7
```

```text
Canceled the working turn of session 7c9e6679-7425-40de-944b-e07fc1f90ae7 with agent reviewer
  prompt: Why does the build fail?
  state:  canceled

The session stays.
Resume this session with:
airo chat -a reviewer -r 7c9e6679-7425-40de-944b-e07fc1f90ae7
```

**The ID in the list is the one every command takes**: `view`, `cancel` and
`airo chat -r`. The resume instructions go to standard error, using the
same format as the closing hint of `airo chat`.

| State | The newest turn |
| --- | --- |
| `queued`, `working` | The agent has it |
| `done` | The agent answered |
| `needs input`, `needs authorization` | The agent waits for you. `cancel` stops this turn too |
| `failed`, `canceled`, `rejected` | The turn ended without an answer |

**You see only your own sessions.** The agents' API keeps a session private
to the person who started it.

**A session is not a resource of the cluster**, so it has no `get` or
`delete`, and `kubectl` does not show it. The commands talk to the agents'
API as [`airo chat`](#chat) does, with the same address and sign-in.

**Without `--agent`, the commands ask every Agent of the project**, because
the API lists sessions for one agent at a time. An agent that does not answer
is a note on standard error, and the rest is printed.

## Tunnel

**`airo tunnel` gives a local port a URL in your project until you stop it**,
like ngrok. The tenant needs tunnels turned on.

```sh
airo tunnel 3000                       # reachable from the cluster, with a token
airo tunnel 3000 --public --no-auth    # a URL on the internet for anyone
```

```text
Tunnel my-project/sam-3000 forwards to localhost:3000
  public:  https://sam-3000.my-project.example.com
  cluster: http://sam-3000.my-project.svc.cluster.local
  callers: anyone who reaches the URL
```

| Argument or flag | Default | Meaning |
| --- | --- | --- |
| `PORT` or `HOST:PORT` | required | The local address to forward to. A port means `localhost` |
| `--name` | `<your username>-<port>` | The Tunnel's name, the first part of its URL |
| `--public` | off | Make the tunnel reachable from the internet too (`spec.public`) |
| `--no-auth` | off | Let callers in without a token (`spec.callerAuth: none`). The tenant needs open tunnels turned on |

**The command creates a Tunnel, serves it, and deletes it on exit.** The
Tunnel it creates has only the fields the flags set, and an expiry:

```sh
kubectl apply -f - <<EOF
apiVersion: tunnels.airohq.dev/v1alpha1
kind: Tunnel
metadata:
  name: sam-3000
  annotations:
    airohq.dev/expires: "2026-11-16T12:15:00Z"   # 15 minutes ahead
spec:
  public: true
  callerAuth: none
EOF
```

**If the command is killed, the cluster deletes the Tunnel within about 15
minutes.** The command moves the expiry 15 minutes ahead every 5 minutes
while it runs. It stops with an error when the Tunnel is gone, such as after
the machine slept past the expiry. Run it again then.

**An existing Tunnel without an expiry is used as it is and stays after
exit.** The command refuses a `--public` or `--no-auth` that differs from it,
so it never exposes someone else's Tunnel. Two people who serve the same
Tunnel share its requests, which is why the default name holds your username.

**An existing Tunnel with an expiry is one a killed `airo tunnel` left
behind.** The command serves it as its own: it moves the expiry forward and
deletes the Tunnel on exit.

**Without `--no-auth`, a caller needs a Kubernetes token and the `call` verb
on the Tunnel.**

**`airo tunnel create` makes a Tunnel that stays**, for a URL that other
people and manifests rely on. On a terminal, omit the name to choose it
and answer the visibility and caller authentication questions. Press Enter
at both questions for a cluster-only URL that requires a token. Supplying
the name skips the wizard. Serve the tunnel with `--name`.

```sh
airo tunnel create                    # open the creation wizard
airo tunnel create my-app --public    # takes --public and --no-auth
airo tunnel 3000 --name my-app        # serves it, and leaves it on exit
airo tunnel list
airo tunnel delete my-app
```

## Develop

```sh
go test -race ./...
go run ./cmd/airo tunnel 3000
```

[DESIGN.md](DESIGN.md) holds how commands are shaped, and
[AGENTS.md](AGENTS.md) the layout and the working rules.

**The workflow `CI` runs on every pull request and on `main`.**

| Job | Checks |
| --- | --- |
| `test` | `go mod tidy`, formatting, `go vet` and the tests |
| `vulnerabilities` | [govulncheck](https://go.dev/doc/security/vuln/): known vulnerabilities the code reaches, in the modules and in Go. Also every Monday, because advisories appear without a change here |
| `build` | Builds every release binary with GoReleaser, without publishing |

Dependabot opens pull requests for new versions of the Go modules and the
workflows' actions.

## Release

**A version tag releases the stable version; `main` is always installable
with Go.**

```sh
git tag v0.1.0 && git push origin v0.1.0
```

The workflow `Release` runs the tests and govulncheck, then
[GoReleaser](https://goreleaser.com): it puts the
binaries on the GitHub release and updates the cask in
[airohq/homebrew-tap](https://github.com/airohq/homebrew-tap). A tag with a
suffix, such as `v0.2.0-rc.1`, becomes a pre-release and leaves Homebrew as it
is. The workflow pushes the cask with the repository secret
`HOMEBREW_TAP_DEPLOY_KEY`, the private half of the deploy key `airo-cli
release` on the tap. To rotate it, add a new deploy key with write access
there, replace the secret, and delete the old key.

## License

[MIT](LICENSE)
