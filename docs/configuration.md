# Provider configuration reference

This reference describes the options in [provider.yaml](../provider.yaml) for
`v0.1.17`. Options are saved on the client running Devsy. Paths refer to that
client, including when Devsy runs on Windows or inside a container.

## Cluster access

| Option | Default | Description |
| --- | --- | --- |
| `KUBECONFIG` | Required | Path to the Harvester kubeconfig on the Devsy client. |
| `HARVESTER_CONTEXT` | Kubeconfig's current context | Optional kubeconfig context for the Harvester cluster. |
| `HARVESTER_NAMESPACE` | `default` | Namespace containing the VM and its disk claims. |
| `HARVESTER_KUBECTL_PATH` | Devsy-managed kubectl | Optional absolute path to an executable kubectl. No kubectl PATH setup is required for normal installs. |

## VM and storage

These settings apply when provisioning a new VM. Reattachment does not rewrite
an existing VM or disk claim. Bundled-profile VMs retain their original readiness
checks through a saved VM annotation; changing this option does not bootstrap
an existing custom VM. See [existing-machine behavior](../README.md#reusing-an-existing-machine).

| Option | Default | Description |
| --- | --- | --- |
| `HARVESTER_IMAGE_CHOICE` | Select an image | The **Image** dropdown shows ready images by Harvester display name. Available after initialization on Devsy v1.24+. An image must be selected before new VM creation. |
| `HARVESTER_IMAGE_NAMESPACE` | `harvester-public` | Namespace whose images appear in the dropdown. |
| `HARVESTER_IMAGE_TYPE` | `raw` | Image attachment type: `raw`, `qcow2`, or `iso`. |
| `HARVESTER_STORAGE_CLASS` | Unset | Required for the separate writable root disk when the image type is `iso`. Other image types use the image's resolved storage class. |
| `HARVESTER_CPU` | `4` | VM CPU cores. |
| `HARVESTER_MEMORY` | `8Gi` | VM memory request. |
| `HARVESTER_DISK` | `40Gi` | Root disk size. |
| `HARVESTER_NETWORK` | Pod network | Existing Harvester network attachment, `NAMESPACE/NAME` or a name in `HARVESTER_NAMESPACE`. The Devsy client must be able to reach the guest. |

`HARVESTER_IMAGE_CATALOG`, `HARVESTER_IMAGE_PIN`, and
`HARVESTER_IMAGE_SELECTION_MODE` are hidden, provider-managed state for catalog
refresh and durable selection. `HARVESTER_IMAGE` is hidden legacy migration state.
Do not configure these manually. The picker needs image-list permission; saved
selections can still be verified with image-GET-only access. See
[image selection](../README.md#image-selection) for first-run setup and refresh.

## Guest bootstrap

The complete cloud-init sources below are mutually exclusive. If none is set,
the provider generates SSH setup using `HARVESTER_SSH_PUBLIC_KEY`. The guest
must provide Docker for Devsy's Docker driver.

| Option | Default | Description |
| --- | --- | --- |
| `HARVESTER_SSH_PUBLIC_KEY` | Unset | Authorized public key used by the generated fallback and bundled profile. Not merged into custom complete sources. |
| `HARVESTER_CLOUD_INIT_PROFILE` | Unset | Opt-in `ubuntu-24.04-docker-v1` for a fresh Ubuntu 24.04 cloud image. Requires the public key, a valid non-root SSH user, and a DNS-label VM name. |
| `HARVESTER_CLOUD_INIT_TEMPLATE` | Unset | Existing Harvester ConfigMap, `NAMESPACE/NAME` or a name in `HARVESTER_NAMESPACE`. Reads `data.cloudInit` using the configured kubeconfig and context. |
| `HARVESTER_USER_DATA_FILE` | Unset | Local path to complete cloud-init user-data. Windows clients require a path valid on Windows. |
| `HARVESTER_USER_DATA` | Unset | Inline complete cloud-init user-data. Marked as a password option in the manifest. |

Custom complete sources must include the intended guest user's authorized public key
and Docker setup. The resolver rejects empty sources, conflicting sources, and
content larger than 64 KiB. ConfigMaps and VM cloud-init are not secret vaults.
Private keys and kubeconfigs stay on the client.

The bundled profile installs Docker Engine/CLI, containerd, Compose, Buildx, and
QEMU guest agent. It allows fifteen minutes for guest readiness, including
successful cloud-init completion; custom sources retain ninety seconds.
See [bundled bootstrap](../README.md#bundled-ubuntu-2404-docker-profile).

See [cloud-init sources](../README.md#saved-harvester-templates-and-local-files)
and [multiline CLI quoting](../README.md#cloud-init-cli-quoting-and-network-access).

## SSH connection

| Option | Default | Description |
| --- | --- | --- |
| `HARVESTER_SSH_HOST` | Discover from VMI | Optional SSH address override. Automatic discovery uses the `default` guest interface, ignoring Docker bridges. |
| `HARVESTER_SSH_USER` | `ubuntu` | SSH user in the Linux guest. |
| `HARVESTER_SSH_PORT` | `22` | SSH port in the guest. |
| `HARVESTER_SSH_FLAGS` | Unset | Additional whitespace-separated OpenSSH arguments. Commands containing spaces require an executable wrapper. |

The matching private key stays on the client or in its SSH agent. Host-key trust
uses the VM's immutable UID, including when the guest IP changes. A changed key
for the same VM remains an error. See [SSH identity](../README.md#ssh-identity-with-dynamic-addresses)
and [network access](../README.md#cloud-init-cli-quoting-and-network-access).

## Agent defaults

The manifest configures Devsy's agent path as `/opt/devsy/agent`, selects the
`docker` driver, and sets an inactivity timeout of `30m`. Its shutdown command is
`sudo -n shutdown -h now || shutdown -h now` in the guest. These are manifest
settings, not additional Harvester provider options.
