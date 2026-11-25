# AGENTS.md

`airo` is the command-line client of the Airo platform. This file is for
coding agents and people changing this repository; keep it short.

## Layout

| Path | Holds |
| --- | --- |
| `cmd/airo` | The entry point: the root command and kubectl's flags. A first word that is no command goes to `internal/generic` |
| `internal/login` | `airo login`, `airo context`, `airo project`, `airo tenant`, `airo status`, `airo whoami`, and `airo credential`, the kubeconfig's exec plugin |
| `internal/generic` | `list`, `get`, `describe`, `edit` and `delete` for any kind in the cluster, plus manifest-based `airo create` and `airo apply` |
| `internal/agent` | `airo chat` for conversations and `airo agent` for resource commands. `a2a.go` holds the whole wire protocol of the agents' API, and `api.go` what other packages get of it |
| `internal/session` | `airo session`: list and view the person's sessions, and cancel a running turn. It reaches the agents' API only through `internal/agent` |
| `internal/skill` | Skill sources as `--skill` takes them, pinned to a Git commit or an image digest |
| `internal/registry` | Public directories of skills that `airo agent skill search` asks, such as skills.sh |
| `internal/wizard` | Terminal prompts for resource creation |
| `internal/picker` | The picker every command shows for a choice from a list |
| `internal/bearer` | The signed-in person's token, for a call to a component's own API |
| `internal/ui` | Terminal detection, colour roles and Markdown rendering shared by commands |
| `internal/tunnel` | `airo tunnel`: the Tunnel resource and the client connection |
| `.goreleaser.yaml` | The release: binaries and the Homebrew cask |
| `.github` | The workflows `CI` and `Release`, and Dependabot |

## Rules

- **The CLI is a thin client of the platform's Kubernetes API.** A command
  builds resources from its flags, sends them as the kubeconfig's identity,
  and does the local work only a client can do, such as holding a tunnel's
  connection. Logic that another client would need belongs in the cluster.
- **Resources go through the Kubernetes API.** What is not a resource, such
  as an agent's sessions, may use a component's own API when its address
  comes from the cluster and the caller is the signed-in person
  ([DESIGN.md](DESIGN.md#apis)). No endpoints that only the CLI knows.
- **Import nothing from the platform's components**, such as tunnel-operator.
  Read their kinds as unstructured objects, and only the fields the command
  needs.
- **Name each kind's group, version and kind in one place.** Airo-owned kinds
  are planned to replace the components' kinds.
- **Tolerate version skew.** Ignore unknown fields, and say so clearly when an
  expected field is missing.
- **A wire protocol's constants are copied and labelled with its version**,
  such as "tunnel client protocol v1". A released binary pins them.
- **Never change a resource the command did not create**, unless changing
  it is the command's task, as `airo agent skill` changes an Agent's skills.
  Then change only those fields.
- **A change in behavior updates the README in the same change.**

## Command design

**Read [DESIGN.md](DESIGN.md) before adding or changing a command.** It holds
the grammar, default tasks, flags, the capabilities a command requires,
output, the look on a terminal, cleanup and which APIs a command may call.

## Checks

```sh
go mod tidy -diff
go vet ./...
go test -race ./...
go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...
```
