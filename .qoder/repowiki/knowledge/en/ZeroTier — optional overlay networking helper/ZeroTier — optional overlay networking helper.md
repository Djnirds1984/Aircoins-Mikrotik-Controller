---
kind: external_dependency
name: ZeroTier — optional overlay networking helper
slug: zerotier
category: external_dependency
category_hints:
    - vendor_identity
    - framework_behavior
scope:
    - '**'
source_files:
    - handlers/tools.go
    - scripts/aircoins-install-zerotier
---

### ZeroTier
- Optional host-side feature exposed under the Tools panel: install `zerotier-cli`, show node/network status, join or leave a validated 16-character ZeroTier network, and list each joined network's tunnel interface plus assigned addresses.
- Installation is delegated to the root-owned helper `/usr/local/sbin/aircoins-install-zerotier`, invoked only from the panel; it pulls from ZeroTier's official `https://install.zerotier.com` script. The helper accepts no arguments and is whitelisted for the `aircoins` user via `/etc/sudoers.d/aircoins-tools`.
- When ZeroTier reports no managed address for a network, the panel falls back to kernel-assigned addresses on that interface.
- This is an optional add-on; the controller boots and operates without it.