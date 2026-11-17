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
| No noun | Only what belongs to no resource: `airo login`, `airo status`, `airo whoami`, `airo apply -f`, `airo version` |

**Every noun takes its plural.** `airo sessions list` is `airo session
list`, and `airo tunnels 3000` is `airo tunnel 3000`. A noun with its own
command names the plural as an alias, and a test fails when one does not. A
kind without a command takes the plural from the cluster. What is not a noun,
such as `airo login`, has one name.

**A noun that is not a kind in the cluster has only hand-written verbs.**
`airo session` is one: a session lives in the agents' API. It has `list`,
because people expect it, and no `get` or `delete`. Without a verb it prints
the help. `airo context` is another: a context lives in the kubeconfig.

**A noun that is the platform's word for a kind has its own verbs too.**
`airo project` is one: a project is a Namespace, and `airo namespace list`
still works for the kind. `airo project list` marks the project the commands
work in.

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
  `airo project acme-web` is the same for the project the commands work in.
- **A verb wins over an argument.** `airo agent list` lists agents. A prompt
  that equals a verb goes after `--`, as in `airo agent -- list`.
- **A missing choice is resolved in the cluster, then by asking.** With no
  `--agent`, `airo agent` takes the project's only agent, then the one marked
  as default with the annotation `airohq.dev/default-agent: "true"`, then
  shows a picker. Where nobody can answer, such as in a script, it stops and
  lists the names. `airo agent create` finds its Harness and ModelConfig the
  same way, with `airohq.dev/default-harness` and `airohq.dev/default-model`,
  and without a picker.
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
- **Every picker is the same one**, from `internal/picker`: the person types
  to filter, Enter chooses and Esc leaves. It opens only on a terminal.

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
- **`airo context set` keeps them too, without signing in.** It takes
  `airo login`'s flags and changes an existing sign-in, as `kubectl config
  set-context` and `set-cluster` do. Only `airo login` makes a sign-in.

## Output

**Output starts with what the person needs next**, such as the URL.

**What a task produces goes to standard output, and notes about it to
standard error.** An agent's answer can be piped, and the session's ID does
not end up in it.

**An ID a command prints is one another command takes as it is.** The
session ID of `airo agent` and `airo session list` goes into `airo session
view`, `airo session cancel` and `airo agent -r`.

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
