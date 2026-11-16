# airo

> [!CAUTION]
> **Early alpha.** Commands and flags may change.

`airo` is the command-line tool of the Airo platform: sign in, manage the
resources of your project, and give a local port a URL. Each command reads
and writes Kubernetes resources as you, so everything it does can also be
done with `kubectl`, and you do not need `kubectl` to use it.

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

**`airo login` signs you in through the browser.** The first time, pass the
kubeconfig file you got from the platform's admin.

```sh
airo login -f airo.kubeconfig    # the first time
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
| First, with `-f` | Copies the file's cluster and context into your kubeconfig, with `airo` in place of kubelogin |
| A customer's first | Sets the context's namespace to the project `<tenant>-default`. A namespace you chose stays |
| Again, without `-f` | Signs in again in the current context, or the one from `--context` |
| With `-n NAMESPACE` | Makes that project the context's namespace, for every later command |
| In no customer group, such as a platform admin | Sets no namespace, so commands use `default` until you pass `-n` |

`airo` takes kubectl's flags, such as `-n`, `--context` and `--kubeconfig`,
and works in the context's namespace by default.

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

**A kind takes the names kubectl takes**: `agent`, `agents`, a short name, or
`agents.api.kagent.dev` when two groups have the same kind. `airo apply` is a
server-side apply with the field manager `airo`.

## Agent

**`airo agent create` makes an agent from a prompt and waits until it is
ready.**

```sh
airo agent create reviewer --prompt "You review pull requests."
airo agent create reviewer --prompt-file prompt.md --model sonnet-5.5
airo agent list
airo agent delete reviewer
```

```text
Created agent my-project/reviewer
  harness:  main (claude)
  model:    sonnet-5.5
  ready
```

| Argument or flag | Default | Meaning |
| --- | --- | --- |
| `NAME` | required | The Agent's name |
| `--prompt` | none | The system prompt (`spec.template.inline.systemPrompt`) |
| `--prompt-file` | none | A file with the system prompt, or `-` for standard input |
| `--description` | none | What the agent is for, in a line (`spec.template.inline.description`) |
| `--model` | the project's only ModelConfig | The model to use (`spec.template.inline.modelConfig.name`) |
| `--harness` | the project's only Harness | What runs the agent (`spec.harnessRef.name`) |
| `--template` | none | An AgentTemplate to use in place of a prompt, a description and a model (`spec.template.ref.name`) |

**The Harness, the ModelConfig and the AgentTemplate exist in the project
already.** `airo` never guesses between several: it stops and lists the
names, and for a Harness what it runs, such as `claude`. A Harness that runs
its own image (`byo`) gets a model only with `--model`.

**The Agent has only the fields the flags set:**

```sh
kubectl apply -f - <<EOF
apiVersion: api.kagent.dev/v1alpha3
kind: Agent
metadata:
  name: reviewer
spec:
  harnessRef:
    name: main
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
