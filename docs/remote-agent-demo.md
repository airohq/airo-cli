# Create an agent for a local sandbox demo

Create the project resources once. On demo day, start the server on your
machine and chat from a second terminal. All commands below use the same Airo
context and the same project namespace. If the project already has a remote
Harness, its Tunnel and its EgressPolicy, skip to **Create the Agent**.

## Temporary agents

With a relay reserved for temporary agents, the presenter runs:

```sh
airo agent serve --harness codex --codex-sandbox read-only --image local/remote-harness:codex-auth-fix
```

With a released worker loaded, use that image tag instead. The server prints
the generated Agent name and a chat command. Ctrl-C deletes the Agent and its
private Tunnel. Losing the process or laptop stops the five-minute expiry
renewals. The cluster janitor removes the Tunnel after its 15-minute expiry,
and Kubernetes garbage collection removes its Agent. The relay configuration
and the local workspace survive. One relay supports one server at a time;
reserve another name for an additional simultaneous server.

**One-time setup for the administrator.** Follow **Prepare the project** below
with a new, dedicated name (for example, `demo=local-serve`). After creating
the four resources, run these two commands and skip **Create the Agent**.
Delete only the new setup Tunnel you just created. This frees the reserved
name for the CLI. Labelling the EgressPolicy opts the relay into unnamed
serving. Use a dedicated demo project if its other agent sessions should not
have access to the local machine.

```sh
airo -n "$project" tunnel delete "$demo"
kubectl -n "$project" label \
  egresspolicy.api.kagent.dev "$demo" airohq.dev/temporary-agent=true
```

The policy and Harness must share the reserved Tunnel name. The CLI verifies
the new Tunnel's caller URL against the Harness before serving. An occupied
name is skipped. A named `--agent` continues to use its existing Tunnel.

## Prepare the project (once, as a project administrator)

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
reference=agent-serve-test-dkj35k  # use a Harness in your project
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

## Create the Agent

The Agent refers to both the relay Harness and its EgressPolicy. The
`airo agent create` wizard can select a Harness. Use a manifest here to
supply `egressPolicyRefs` as well. This BYO relay can use Codex's default
model, so the Agent can omit a ModelConfig.

```sh
project=airohq-default
agent=laptop-demo
remote=laptop-demo  # use agent-serve-test-dkj35k to reuse the existing test setup
airo -n "$project" create -f - <<YAML
apiVersion: api.kagent.dev/v1alpha3
kind: Agent
metadata: {name: "$agent"}
spec:
  harnessRef: {name: "$remote"}
  egressPolicyRefs: [{name: "$remote"}]
  template:
    inline:
      systemPrompt: "You are a concise coding assistant for this demo."
YAML
airo -n "$project" agent list
```

Agents using the same relay share its Tunnel and local workspace. Run one
server for the Tunnel. Provision a separate Tunnel and relay for an independent
workspace.

## Run the demo

In the serving terminal, export `OPENAI_API_KEY` or `CODEX_API_KEY`. Keep the
credential on this machine. Load the released worker image into the container
engine before serving. For the locally tested build, use
`worker=local/remote-harness:codex-auth-fix` and skip the pull.

```sh
worker="ghcr.io/airohq/kagent-remote-harness/runner:${release:?set the shipped worker tag}"
docker pull "$worker"
airo -n "$project" agent serve -a "$agent" --harness codex --sandbox container \
  --codex-sandbox read-only --image "$worker" -v
```

In a second terminal, use the same namespace and Agent name.

```sh
airo -n airohq-default chat -a laptop-demo 'What is 17 times 23?'
```

The first turn starts the local container. Ctrl-C in the serving terminal
stops serving. The project resources persist for the next demo.
