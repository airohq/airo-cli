# Create an agent for a local sandbox demo

Use the same Airo context and project namespace in both terminals.

## Create a durable Agent

In a project with agents, tunnels and the local relay creation permissions, run:

```sh
project=airohq-default
agent=laptop-demo
airo -n "$project" agent create "$agent" --local --prompt "You are a concise coding assistant."
```

The CLI creates the Agent and its private relay. Continue with **Run the demo**.
After disconnecting, you can serve the same Agent again. To retire it, run
`airo -n "$project" agent delete "$agent"`. Kubernetes removes its relay resources.

## Temporary agents

With a relay reserved for temporary agents, the presenter runs:

```sh
airo agent serve --name demo
```

Omit `--name` for a generated name. The server prints
`Created temporary agent NAME`. Use `airo chat -a NAME` in the same project.
Ctrl-C or `airo agent delete NAME` deletes the temporary Agent. Kubernetes
then removes its private Tunnel. If the laptop goes offline, the janitor
deletes the Agent after its 15-minute deadline and the same cascade applies.
The relay configuration survives. Each new temporary Agent gets a fresh local workspace. One relay supports one server at a time;
reserve another name for an additional simultaneous server.

**One-time setup for the administrator.** Follow **Prepare the project** below
with a new, dedicated name (for example, `demo=local-serve`). After creating
the relay resources, run these two commands.
Delete only the new setup Tunnel you just created. This frees the reserved
name for the CLI. Labelling the EgressPolicy opts the relay into temporary
serving. Use a dedicated demo project if its other agent sessions should not
have access to the local machine.

```sh
airo -n "$project" tunnel delete "$demo"
kubectl -n "$project" label \
  egresspolicy.api.kagent.dev "$demo" airohq.dev/temporary-agent=true
```

The policy and Harness must share the reserved Tunnel name. The CLI verifies
the new Tunnel's caller URL against the Harness before serving. An occupied
name is skipped. Use `airo agent connect NAME` for an existing Agent and its Tunnel.

## Prepare the project for temporary Agents (once, as a project administrator)

This step requires permission to create Roles, RoleBindings, EgressPolicies,
Harnesses and Tunnels. Use a dedicated demo project with agents and tunnels enabled. The person
serving the agent needs `connect` on its Tunnel. The setup below grants the
project's agent sessions `call` on that one Tunnel. Any such session with
access to the Tunnel can send turns to this machine.

Set `project` to the Kubernetes namespace and choose a new demo name. In
the example, `airohq-default` is the project used for the original test.
Choose an existing Harness in **your** project whose worker pool and snapshot
storage are suitable for the in-cluster relay.

```sh
project=airohq-default
demo=laptop-demo
airo -n "$project" harness list
```

Set `reference` to a name from that list. Read its substrate settings and
create the persistent, private Tunnel. The creation command waits for a URL.

```sh
reference=default  # select a Harness from the list above
pool=$(airo -n "$project" harness get "$reference" -o jsonpath='{.spec.substrate.workerPoolRef.name}')
storage=$(airo -n "$project" harness get "$reference" -o jsonpath='{.spec.substrate.snapshotPolicy.location}')
airo -n "$project" tunnel create "$demo"
origin=$(airo -n "$project" tunnel get "$demo" -o jsonpath='{.status.callerURL}')
```

If either substrate field is empty, get the worker pool name and snapshot
location from the project administrator. The image below is the in-cluster
relay digest used in the working demo. Use the published relay digest for the
release you are demonstrating.

```sh
relay=ghcr.io/airohq/kagent-remote-harness@sha256:a75eef68c5a4468d3dbe1e60d4202bea4399007e8dd5e520dc118c5d94aca31b
airo -n "$project" create -f - <<YAML
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata: {name: "$demo"}
rules:
  - apiGroups: [tunnels.airohq.dev]
    resources: [tunnels]
    resourceNames: ["$demo"]
    verbs: [call]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata: {name: "$demo"}
roleRef: {apiGroup: rbac.authorization.k8s.io, kind: Role, name: "$demo"}
subjects:
  - {apiGroup: rbac.authorization.k8s.io, kind: Group, name: "substrate:atespace:$project"}
---
apiVersion: api.kagent.dev/v1alpha3
kind: EgressPolicy
metadata: {name: "$demo"}
spec:
  destinations:
    - origin: "${origin:?read the ready Tunnel caller URL}"
      authentication:
        header: Authorization
        prefix: "Bearer "
        actorJwt: {audiences: ["$origin"]}
---
apiVersion: api.kagent.dev/v1alpha3
kind: Harness
metadata: {name: "$demo"}
spec:
  byo: {}
  workload: {image: "$relay", command: ["/harness"]}
  env:
    - {name: REMOTE_HARNESS_URL, value: "$origin"}
  substrate:
    workerPoolRef: {name: "${pool:?set the project worker pool}"}
    snapshotPolicy: {location: "${storage:?set the project snapshot location}"}
YAML
```

## Run the demo

For a ChatGPT subscription, unset `OPENAI_API_KEY` and `CODEX_API_KEY` in the
serving terminal. Follow the device sign-in prompt on the first run. To use an API key, export either variable instead. On the first turn, the CLI pulls the released
worker image from GHCR and uses its image ID until the server exits.

```sh
airo -n "$project" agent connect "$agent" --harness codex --sandbox container \
  --codex-sandbox read-only -v
```

In a second terminal, use the same namespace and Agent name.

```sh
airo -n airohq-default chat -a laptop-demo 'What is 17 times 23?'
```

The first turn starts the local container. Ctrl-C in the serving terminal
disconnects the machine. Reconnect later with the same connect command. The
Agent and Tunnel keep their identities. To continue the conversation, run
`airo -n airohq-default chat -a laptop-demo -c` from the same client.
