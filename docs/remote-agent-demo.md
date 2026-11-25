# Create an agent for a local sandbox demo

Use the same Airo context and project namespace in both terminals.

## Create a durable Agent

In a project with the agents, tunnels and local-agents capabilities, run:

```sh
project=airohq-default
agent=laptop-demo
airo -n "$project" agent create "$agent" --local --prompt "You are a concise coding assistant."
```

The CLI creates the Agent and its private relay. Continue with **Run the demo**.
After disconnecting, you can serve the same Agent again. To retire it, run
`airo -n "$project" agent delete "$agent"`. Kubernetes removes its relay resources.

## Temporary agents

In the same kind of project, the presenter runs:

```sh
airo agent serve --name demo
```

Omit `--name` for a generated name. The CLI creates the Agent and its own
private relay, as `agent create --local` does, and prints
`Created temporary agent NAME`. Use `airo chat -a NAME` in the same project.
Ctrl-C or `airo agent delete NAME` deletes the temporary Agent. Kubernetes
then removes its relay. If the laptop goes offline, the janitor deletes the
Agent after its 15-minute deadline and the same cascade applies. Each
temporary Agent gets a fresh local workspace, and several people can serve
at the same time. No administrator setup is needed.

## Run the demo

For a ChatGPT subscription, unset `OPENAI_API_KEY` and `CODEX_API_KEY` in the
serving terminal. Follow the device sign-in prompt on the first run. To use an API key, export either variable instead. On the first turn, the CLI pulls the released
worker image from GHCR and uses its image ID until the server exits. Codex
runs without its own sandbox; the worker container is the security boundary
(see the README's local sandbox settings).

```sh
airo -n "$project" agent connect "$agent" --harness codex --sandbox container -v
```

In a second terminal, use the same namespace and Agent name.

```sh
airo -n airohq-default chat -a laptop-demo 'What is 17 times 23?'
```

The first turn starts the local container. Ctrl-C in the serving terminal
disconnects the machine. Reconnect later with the same connect command. The
Agent and Tunnel keep their identities. To continue the conversation, run
`airo -n airohq-default chat -a laptop-demo -c` from the same client.
