# airo

> [!CAUTION]
> **Early alpha.** Commands and flags may change.

`airo` is the command-line tool of the Airo platform: sign in, switch between sign-ins, talk to an
agent, see your sessions, manage the resources of your project, and give a local port a URL.
Each command reads and writes Kubernetes resources as you, so everything it
does to a resource can also be done with `kubectl`, and you do not need
`kubectl` to use it.

## Install

**Install and update the stable release with Homebrew, on macOS and Linux.**

```sh
brew install airohq/tap/airo
brew upgrade airo        # update
airo --version
```

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
| With `-n NAMESPACE` | Makes that project the context's namespace, for every later command |
| In no customer group, such as a platform admin | Sets no namespace, so commands use `default` until you pass `-n` |

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
`airo` and `kubectl`. `airo ctx` is the short form.

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
| `airo context` | Shows a picker with the cursor on the current context. Type to filter, press Enter to choose, or Esc to leave things as they are. Where nobody can answer, such as in a script, it stops and lists the names |
| `airo context NAME` | Makes that context the kubeconfig's current one |
| `airo context -` | Switches to the context you last switched away from, so twice brings you back. `airo` keeps its name in your cache directory |
| `airo context list` | Lists the sign-ins with their namespaces, and marks the current one with `*` |

**It knows only the contexts that sign in through the platform.** Others,
such as a local cluster's, are not listed and not switched to. Use `kubectl
config use-context` for them.

A context named like a verb goes after `--`, as in `airo context -- list`.

## Resources

**Every kind of resource in the cluster is a noun with `list`, `get` and
`delete`**, also kinds this version of `airo` does not know. `airo` asks the
cluster for the kind and its columns.

```sh
airo agent list                    # in your project
airo agent list -A                 # in every namespace you may read
airo agent get my-agent            # one row
airo agent get my-agent -o yaml    # the whole resource, or -o json
airo agent delete my-agent
airo apply -f agent.yaml           # create or update from a file, a directory, a URL or -
```

**A noun works in the singular and the plural.** `airo agents list` is `airo
agent list`, and the same holds for `airo sessions` and `airo tunnels`.

**A kind takes the names kubectl takes**: `agent`, `agents`, a short name, or
`agents.api.kagent.dev` when two groups have the same kind. `airo apply` is a
server-side apply with the field manager `airo`.

## Agent

**`airo agent` sends a prompt to an agent of your project and prints the
answer as it arrives.** The prompt starts a new session.

```sh
airo agent "Why does the build fail?"    # a new session
airo agent -c "Fix it."                  # the next prompt of the same session
airo agent                               # a conversation, on a terminal
```

```text
The build fails in the step lint: main.go has an unused import.
Session 7c9e6679-7425-40de-944b-e07fc1f90ae7
  continue: airo agent -a reviewer -c "PROMPT"
```

| Argument or flag | Default | Meaning |
| --- | --- | --- |
| `PROMPT` | a conversation | What to ask the agent. Without it, each line you type is a prompt of one session, until ctrl-D. Input that is not a terminal, such as a pipe, is one prompt |
| `-a`, `--agent` | see below | The Agent's name |
| `-c`, `--continue` | off | Send the prompt to your last session with the agent |
| `-r`, `--resume` | none | Send the prompt to the session with this ID |

**Without `--agent`, the cluster decides which agent answers.** `airo` takes
the first that applies:

1. The project's only Agent.
2. The Agent with the annotation `airohq.dev/default-agent: "true"`. When
   several have it, the one created last: mark your own agent to use it in
   place of the platform's `assistant`.
3. The one you choose in a picker, on a terminal: type to filter and press
   Enter. Elsewhere the command stops and lists the names.

**A session stays in the cluster after the command ends.** Nothing is
deleted. `--resume` takes the session's ID, which every run prints and
[`airo session list`](#session) shows.
`--continue` takes the last session this machine started or resumed with
the agent. `airo` remembers one ID for each agent in `airo/sessions.json` in
your cache directory.

**Ctrl-C detaches, and the agent works on in the cluster.** The command
prints how to come back and exits without an error:

```text
Detached. The agent works on in the cluster.
  resume: airo agent -a reviewer -r 7c9e6679-7425-40de-944b-e07fc1f90ae7
```

**The answer goes to standard output and everything else to standard
error**, so the answer can be piped. The command fails when the agent fails,
turns the prompt down or the turn is canceled. In a conversation it prints
the failure and takes the next prompt.

**A prompt that equals a verb goes after `--`**, as in `airo agent -- list`.

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

**`airo agent create` makes an agent from a prompt and waits until it is
ready.**

```sh
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
| `NAME` | required | The Agent's name |
| `--prompt` | none | The system prompt (`spec.template.inline.systemPrompt`) |
| `--prompt-file` | none | A file with the system prompt, or `-` for standard input |
| `--description` | none | What the agent is for, in a line (`spec.template.inline.description`) |
| `--model` | see below | The ModelConfig to use (`spec.template.inline.modelConfig.name`) |
| `--harness` | see below | The Harness that runs the agent (`spec.harnessRef.name`) |
| `--template` | none | An AgentTemplate to use in place of a prompt, a description and a model (`spec.template.ref.name`) |

**The Harness, the ModelConfig and the AgentTemplate exist in the project
already.** The platform puts Harnesses and ModelConfigs there, and `airo`
creates neither. Without `--harness` or `--model`, `airo` takes the first
that applies:

1. The project's only one.
2. The one marked as the default: a Harness with the annotation
   `airohq.dev/default-harness: "true"`, a ModelConfig with
   `airohq.dev/default-model: "true"`. When several have it, the one created
   last.

Otherwise it stops and lists the names, and for a Harness what it runs, such
as `claude`. A Harness that runs its own image (`byo`) gets a model only
with `--model`.

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

## Session

**`airo session` shows your sessions with the agents of your project.** A
session is a conversation with one agent. `airo agent` starts one and prints
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
  resume: airo agent -a reviewer -r 7c9e6679-7425-40de-944b-e07fc1f90ae7
```

```text
Canceled the working turn of session 7c9e6679-7425-40de-944b-e07fc1f90ae7 with agent reviewer
  prompt: Why does the build fail?
  state:  canceled
The session stays.
  resume: airo agent -a reviewer -r 7c9e6679-7425-40de-944b-e07fc1f90ae7
```

**The ID in the list is the one every command takes**: `view`, `cancel` and
`airo agent -r`. The lines that start with `resume:` go to standard error.

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
API as [`airo agent`](#agent) does, with the same address and sign-in.

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
people and manifests rely on. Serve it with `--name`.

```sh
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
