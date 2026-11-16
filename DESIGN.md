# Design

How `airo`'s commands are shaped, for people and coding agents adding or
changing one. `airo` may be the only tool a person has, so it covers both
daily tasks and managing resources, without kubectl.

## Grammar

**Every command is `airo <noun> [verb] [argument]`**, like `gh issue list`.
The noun is a kind of resource, in the singular.

```sh
airo tunnel 3000             # the noun's default task
airo tunnel list             # a verb
airo tunnel delete my-app
airo agent "fix the build"   # the default task of another noun
```

| Part | Rule |
| --- | --- |
| Noun | A kind of resource, such as `tunnel` or `agent` |
| No verb | The noun's default task, if it has one. Otherwise the help |
| `list`, `get`, `delete` | Work for every kind the cluster serves, also kinds this binary does not know |
| `create` | Hand-written for each noun, with flags |
| Other verbs | Actions of one noun, such as `airo agent logs` |
| No noun | Only what belongs to no resource: `airo login`, `airo apply -f`, `airo version` |

## Default task

**A noun's default task is the one thing most people want from it**: `airo
tunnel 3000` serves a local port, `airo agent` starts a fresh session.

- **Its argument is what the task acts on, never a resource's name.** It is
  the port for a tunnel and the prompt for an agent. A resource's name is a
  flag, such as `--name` or `--agent`.
- **A verb wins over an argument.** `airo agent list` lists agents. A prompt
  that equals a verb goes after `--`, as in `airo agent -- list`.
- **A missing choice is resolved in the cluster, then by asking.** With no
  `--agent`, `airo agent` takes the project's only agent, then the one marked
  as default, then shows a picker.

## Arguments and flags

- **Everything but the one argument is a flag with a default that works**, so
  the shortest form does something useful.
- **A flag is named for what the person gets**, such as `--public` and
  `--no-auth`. The README names the field it sets.
- **kubectl's flags mean the same here**: `-n`, `--context`, `--kubeconfig`,
  and `-o yaml` on `get` and `list`.

## Output

**Output starts with what the person needs next**, such as the URL.

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

- **The address comes from the cluster**, such as a resource's status, not
  from a flag or a built-in URL.
- **The caller is the person signed in with `airo login`.** No self-declared
  identity.
