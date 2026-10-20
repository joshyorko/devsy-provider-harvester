# Harvester provider for Devsy

`devsy-provider-harvester` provisions Devsy machine providers as Harvester
VirtualMachine resources and connects to the resulting Linux VM over SSH.
Devsy downloads the provider helper from the version-pinned GitHub release described by
`provider.yaml`.

## Getting started

You need a Harvester kubeconfig, an existing guest image, and SSH access from
your Devsy client to the VM. The Linux guest must provide SSH, Docker, and the
authorized public key for your SSH user. See [client prerequisites](#desktop-and-windows-prerequisites)
and [cloud-init configuration](#saved-harvester-templates-and-local-files) before
adding the provider.

### Install or update

Use the canonical GitHub source (without an `https://` prefix) so Devsy can
discover releases and consume the checksummed release manifest:

```sh
devsy provider add github.com/joshyorko/devsy-provider-harvester
# Preserve an existing registration and its saved options:
devsy provider set-source harvester github.com/joshyorko/devsy-provider-harvester --version v0.1.17 --use=false
devsy provider init harvester
devsy provider versions harvester --json --no-cache
```

The release asset `provider.yaml` includes per-platform SHA-256 checksums;
the repository manifest is the release template, not the recommended install URL.
If you use Devsy's **Reuse machine** setting, pass `--single-machine` to
`provider init`; its default selects a separate machine per workspace.

### Create your first workspace

From your project's directory, run:

```sh
devsy workspace up . --provider harvester
```

Devsy provisions a Harvester VM and runs the workspace's devcontainer inside
that VM with its Docker driver. The first launch waits for the guest IP, SSH,
and Docker before starting the workspace. No separate KubeVirt driver is needed.

## Desktop and Windows prerequisites

Windows amd64, Linux amd64/arm64, and macOS amd64/arm64 helpers are packaged.
Devsy automatically downloads a pinned, checksummed `kubectl` for your platform;
no kubectl PATH setup is required. Install OpenSSH on the client. To override
the managed kubectl, set `HARVESTER_KUBECTL_PATH` to an absolute executable path.
Use a kubeconfig path valid on that client; kubeconfigs and private SSH keys are
not distributed with the provider. Windows uses the same Linux SSH/Docker guest.

## Options

The [configuration reference](docs/configuration.md) lists every provider option
and its default from [provider.yaml](provider.yaml). Set options when adding the
provider with `-o KEY=VALUE`, or change the saved options later:

```sh
devsy provider set harvester -o HARVESTER_CPU=8 -o HARVESTER_MEMORY=16Gi
```

Image, CPU, memory, disk, network, and cloud-init changes apply to new VMs. They
do not resize or reconfigure an existing VM.

## Image selection

On **Devsy Desktop v1.24 or newer**, configure the kubeconfig, context and
`HARVESTER_IMAGE_NAMESPACE`, initialize the provider, then reopen its options.
Use the **Image** dropdown to choose an imported, ready image by its Harvester
display name, such as `rocky9` or `ubuntu-noble`. Internal image IDs stay hidden.
On a new installation the wizard does not load dynamic choices before
initialization, so this is a two-step setup. No image is selected automatically,
even when only one is ready.

The selection saves the exact image reference internally and survives provider
updates. Existing saved image values migrate into the dropdown when they match
an available image. Changing a display name does not switch the selected image.
Choose **Select an image** to clear the selection. A new VM requires a selection;
there is no separate manual image field. Images with duplicate display names
must be renamed in Harvester before selecting them.

Catalog discovery needs permission to list Harvester images in the selected
namespace, but no guest SSH access. If discovery fails, the selector reports
**catalog unavailable** and retains a saved image when it can verify that image
with GET access. Connection errors stop refresh rather than clearing the saved
selection. Reinitialize or save provider options to refresh the catalog; edits
to unsaved connection fields do not refresh it live. Changing the image namespace
clears a selection that is absent from the new catalog.

The released helper also provides a read-only catalog command. For example, on a
Linux client after downloading the helper:

```sh
KUBECONFIG=/path/to/harvester.yaml HARVESTER_IMAGE_NAMESPACE=default \
  ./harvester-provider-linux-amd64 images
```

Add `--json` for display names, exact references and readiness in machine-readable
form. This command reports API errors instead of returning a successful empty list.

The provider resolves the selected image's Harvester
storage class before creating an image-backed PVC and attaching it to the
KubeVirt VM. Set `HARVESTER_IMAGE_TYPE` to `raw`, `qcow2`, or `iso`; ISO images
are attached as a CD-ROM alongside a separate writable root disk. Supply
`HARVESTER_SSH_PUBLIC_KEY` or complete `HARVESTER_USER_DATA` so cloud-init can
bootstrap the guest; the guest image must provide SSH and Docker for the
initial `driver: docker` configuration.

### Saved Harvester templates and local files

Choose **one** complete cloud-init source:

- `HARVESTER_CLOUD_INIT_PROFILE=ubuntu-24.04-docker-v1`: opt-in bundled
  bootstrap for a fresh Ubuntu 24.04 cloud image. See below.

- `HARVESTER_CLOUD_INIT_TEMPLATE=default/ubuntu-docker`: load the existing
  Harvester ConfigMap's `data.cloudInit` using the provider's `KUBECONFIG` and
  `HARVESTER_CONTEXT`. A bare name uses `HARVESTER_NAMESPACE`.
- `HARVESTER_USER_DATA_FILE=C:\Users\you\.config\ubuntu-docker.yaml`: read a file
  on the client running Devsy. Windows paths are not paths on the cluster.
- `HARVESTER_USER_DATA`: inline cloud-init content, as before.

For example (PowerShell or another shell):

```sh
devsy provider set harvester -o HARVESTER_CLOUD_INIT_TEMPLATE=default/ubuntu-docker
```

Clear any previously configured alternate source; multiple sources fail rather
than silently selecting one. Missing/empty templates and files fail during
provider init and before VM/PVC creation. Template contents are fetched again
when creating a VM, not when starting an existing VM. Existing VMs are not
reconfigured when the template changes. Source contents are not printed by the
resolver. ConfigMaps and VM cloud-init are not secret vaults: do not put private
keys, kubeconfigs or API credentials in them.

A complete source must include the intended guest SSH user's authorized public
key and Docker setup. `HARVESTER_SSH_PUBLIC_KEY` is used by the bundled profile and the generated
fallback; it is not merged into custom complete sources.
The matching private key stays on the client or in its SSH agent.

### Cloud-init CLI quoting and network access

`devsy machine create --provider-option` parses each argument as CSV. Wrap a
complete `HARVESTER_USER_DATA=...` value in CSV quotes (double embedded quotes),
including all newlines, rather than passing unquoted multiline text. Workspace
`up --provider-option` uses an array flag and does not need this CSV layer.

Set `HARVESTER_NETWORK` to an existing Harvester network attachment reachable
from your Devsy client, for example:

```sh
devsy provider set harvester -o HARVESTER_NETWORK=default/mgmt-untagged
```

This selects the VM's network when creating it; it does not reconfigure existing
VMs. A bare attachment name uses `HARVESTER_NAMESPACE`. The attachment is checked
during init and before disk/VM creation. Guest IP discovery uses KubeVirt's
`ipAddress` on the provider's `default` interface, ignoring guest Docker bridges.

Without this option, the VM uses the pod network. Your client must route to its
IP, or use a scoped SSH proxy. For example, an executable wrapper around
`virtctl --kubeconfig ... --context ... port-forward --stdio=true vm/NAME/NAMESPACE 22`
can be supplied with `HARVESTER_SSH_HOST=127.0.0.1` and
`HARVESTER_SSH_FLAGS=-o ProxyCommand=/absolute/path/to/wrapper`. This uses the
authenticated Kubernetes API without exposing a node port or changing cluster
network policy. SSH flags currently accept simple whitespace-separated tokens;
put commands containing spaces in a wrapper.

### SSH identity with dynamic addresses

SSH host keys are tracked under `devsy-harvester-<VM UID>` in the client's
normal `known_hosts` file. The provider obtains the immutable VM UID through
the authenticated Harvester API for both readiness probes and workspace SSH
connections. A new VM can use a previously assigned IP without colliding with
that IP's old host-key entry. The same VM keeps its trust identity when its
address changes; no static IP reservation is needed.

The first connection to a VM uses OpenSSH's `accept-new` policy. A changed key
for that same VM remains an error, and readiness stops immediately with the SSH
diagnostic. Verify the VM's key before updating its trust entry. The provider
does not remove old keys or disable host-key verification. Existing IP-based
entries are preserved; after upgrading, the first connection records the new
UID-based entry. If VM identity lookup fails, SSH does not connect.

### Reusing an existing machine

`create` first checks for an existing VM with the requested machine name. It
reuses the VM only when both `devsy.sh/provider=harvester` and
`devsy.sh/machine-id=NAME` labels match. Reattachment leaves the VM and PVC
definitions untouched, preserving Harvester's assigned MAC address and adjusted
memory request. Image, CPU, memory, disk, network and cloud-init options apply
when provisioning a new VM; changing them does not resize or reconfigure an
existing one. Creation-only image and cloud-init sources need not remain
available for reattachment. SSH credentials and Docker readiness are still
checked. A stopped VM is started through KubeVirt's start subresource.

An existing VM without matching ownership labels, a VM being deleted, or a
failed lookup causes an error before provisioning. New VM creation uses an
atomic create operation: if another VM appears after the initial lookup, the
command fails instead of overwriting it; retry to check whether it can be reused.
Disk claims are also created atomically. A retry after partial provisioning can
reuse a claim only when its provider/machine labels, image, storage class, size
and mode match; the claim is never rewritten. A legacy unlabeled orphan claim
without a VM must be inspected and recovered separately. This does not change
Devsy's cleanup of interrupted workspace creation.

Devsy can remove its local machine record after interrupted creation while
leaving the VM behind. A subsequent workspace may reference that missing record.
A worker can also exit with an error while its task remains pending or Checking.
Provider reattachment can reuse an owned VM, but it cannot repair Devsy's local
records or task state. These cancellation and task-state limitations remain in
Devsy; successful provider cleanup does not guarantee cancellation recovery.

### Launch progress and readiness

The provider writes `harvester:` progress messages to Devsy's existing launch
logs during initialization, image lookup, disk provisioning, VM boot, and guest
readiness. VM state is logged when it changes. A running VM still needs a
reachable guest IP, working SSH credentials, and Docker before its workspace can
start. Each SSH/Docker readiness probe is bounded to ten seconds within the
ninety-second guest-readiness wait, or fifteen minutes with the bundled profile; timeout errors retain the last
failed check. Bundled-profile readiness errors redact guest SSH output; inspect
guest logs directly for bootstrap details. Custom sources retain their SSH
diagnostics. Host-key verification remains
a distinct immediate failure. Long-lived workspace SSH transports have no added deadline.

These messages populate the log pane. Devsy currently wraps external machine
provider output as logs, so this provider-only integration cannot add structured
Launch activity timeline steps. Status responses and SSH command output remain
free of progress messages; cloud-init content and credentials are not logged by
the progress reporter.

Deletion waits for the VM and its dependent resources using foreground
propagation, then attempts all expected disk-claim deletions. Any actual cleanup
failure makes the provider command fail; missing resources remain safe to retry.
Completion logs confirm VM and PVC removal, not physical backing-storage
reclamation. Devsy's explicit force-delete behavior can still remove its local
record after a provider failure; this provider does not change that policy.

## Status

The provider uses `kubectl` plus the Harvester KubeVirt API, keeping
provisioning separate from Devsy's workspace driver. It discovers the VM IP
from the VMI when `HARVESTER_SSH_HOST` is not supplied. No separate KubeVirt
driver is required for this path.

## Development

```sh
go build -o harvester-provider ./cmd/harvester-provider
go test ./...
python3 -m unittest discover -s scripts -p 'test_*.py'
```

The build command creates a local helper. Normal Devsy installs download the
matching helper from the GitHub release.

The repository keeps the provider manifest at the root and the helper under
`cmd/`, with release packaging separate from runtime code:

| Path | Purpose |
| --- | --- |
| [provider.yaml](provider.yaml) | Provider options, lifecycle commands, and pinned binary URLs. |
| [cmd/harvester-provider/](cmd/harvester-provider/) | Go helper and its tests. |
| [scripts/](scripts/) | Release manifest and checksum generation, with packaging tests. |
| [.github/workflows/](.github/workflows/) | CI and release workflows. |
| [docs/configuration.md](docs/configuration.md) | Provider option reference. |
| [docs/superpowers/specs/](docs/superpowers/specs/) | Original design notes. |

## Dependency updates

[Patchraptor](https://github.com/joshyorko/renovate-config) discovers this
repository and supplies its inherited Renovate policy. [renovate.json](renovate.json)
opts this repository into that runner. Pull requests run Go tests and vet on
Linux and Windows, plus Linux release-packaging checks, through
[CI](.github/workflows/ci.yml). Auto-merge follows the shared policy; GitHub
Actions version bumps remain reviewable PRs, while eligible pin and digest
updates can auto-merge after the required checks pass.

### Bundled Ubuntu 24.04 Docker profile

For a **new** VM backed by an Ubuntu 24.04 cloud image, select
`HARVESTER_CLOUD_INIT_PROFILE=ubuntu-24.04-docker-v1` and provide
`HARVESTER_SSH_PUBLIC_KEY`. The default remains disabled. Image selection stays
explicit; this profile does not import images or install an OS from an ISO.
Unset any complete inline, file, or ConfigMap source before selecting a profile.
Conflicts fail without merging or overwriting saved custom configuration.
Existing VMs are reused without applying changed cloud-init options. Their
original profile is recorded on the VM and controls readiness checks, even if
you later change the provider setting. Existing custom VMs keep their original
SSH/Docker readiness checks. ISO images are rejected before disk creation when
a bundled profile is selected.

The helper embeds the same profile on Linux amd64/arm64, macOS amd64/arm64,
and Windows amd64. Bundled user-data is gzip-compressed and base64-encoded to
fit KubeVirt's 2 KiB decoded inline limit. Oversized keys or key comments fail
before provisioning; custom source encoding is unchanged. It installs Docker Engine, CLI, containerd, Buildx, Compose,
and QEMU guest agent using Docker's Ubuntu repository. The configured SSH user
must be non-root and is granted passwordless sudo and Docker group access.
Docker group membership grants root-equivalent control of the guest. The public
key must be a single Ed25519, RSA, or ECDSA key. The VM name becomes the hostname
and must be a DNS label of at most 63 characters.

The guest needs DNS and outbound HTTPS to Ubuntu and `download.docker.com`.
Bootstrap refuses other operating systems and is safe to rerun: repository files
are replaced, package installation converges, and Docker membership is additive.
Its script has a fourteen-minute deadline with a thirty-second kill grace. The provider allows fifteen minutes
for guest IP, SSH, successful cloud-init completion, the active QEMU guest agent,
Docker access as the SSH user, and Compose/Buildx. Each probe remains bounded to
ten seconds. Bootstrap stages and package diagnostics remain in the guest's
`/var/log/cloud-init-output.log`; they never enter status or workspace SSH output.
Terminal cloud-init failures stop readiness immediately with diagnostic guidance.
On timeout, inspect that file and `cloud-init status --long` in the guest. A retry
reuses the VM rather than overwriting its disk or rerunning successful cloud-init.

Run `scripts/check_cloudinit.sh` to validate synthetic generated user-data with
Ubuntu 24.04's cloud-init schema in a disposable Docker container. This downloads
Ubuntu packages into the container, not the host. The check does not boot a VM.
Before release acceptance, use a separate disposable Harvester VM to prove
cloud-init completion, guest-agent IP reporting, key-based SSH, non-root Docker,
Compose/Buildx, and an actual project devcontainer. Never use an existing
operator VM for this check.

References: [Docker's Ubuntu installation guide](https://docs.docker.com/engine/install/ubuntu/)
and [cloud-init schema validation](https://cloudinit.readthedocs.io/en/latest/howto/debug_user_data.html).
