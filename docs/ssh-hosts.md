# SSH access to hosts (design, phase 1)

Status: **proposal, not implemented.** This document is the design to approve
before any code is written. It extends tiphys from Kubernetes workloads to
plain hosts (VMs, bare metal) reached over SSH, with the same authorization,
audit and recording guarantees as exec.

## Goal and scope

Phase 1 delivers:

- An **interactive shell in the browser** on a registered host, recorded like
  exec (asciicast segments in MinIO, live watch, terminate, replay).
- Two actions, `ssh` and `ssh-admin`, that are granted, requested, limited by
  time windows and holidays, and overridden by break-glass exactly like the
  Kubernetes actions.
- Hosts **registered from lists in Secrets**, like clusters. The portal only
  reads them.
- **No agent on the hosts.** tiphys connects with OpenSSH's own certificate
  support. A playbook prepares a host once.

Not in phase 1: CLI (`tsh`-like client), file transfer, port forwarding, jump
hosts, typed verbs without a shell (journal, `systemctl restart`, runbooks).
The design leaves room for each (see [Later](#later)).

## Decisions

| Topic | Decision |
|---|---|
| Interface | Web terminal only |
| Portal → host authentication | Per-session SSH user certificate signed by a tiphys CA |
| CA | Created by the operator with `ssh-keygen`, stored in an annotated Secret; tiphys only reads it |
| Host → portal authentication | Host key pinned from the list, or approved by an admin |
| Actions | `ssh` (account `tiphys`), `ssh-admin` (account `tiphys-admin`) |
| Grant scopes | New `host` and `host_group`; `environment` covers namespaces **and** hosts |
| Time windows, holidays, break-glass | Same behavior as Kubernetes; new window scope `host_group` |
| Registration | Lists of hosts in Secrets annotated `tiphys/hosts` |
| Management UI | Admin only, a Hosts section on Clusters & environments |
| Source address restriction | Optional, on both sides (playbook and certificate) |

## How a session works

```
browser ── WebSocket ──► portal ──────────────── SSH ─────────────► host (OpenSSH)
                           │ Decide(host, ssh|ssh-admin)            │
                           │ audit ssh.start, recording segment 0   │ ~tiphys/authorized_keys:
                           │ new ed25519 key + certificate          │ cert-authority,principals="tiphys" <CA>
                           │   principal  = tiphys | tiphys-admin   │
                           │   key ID     = tiphys:<user>:<session> │ sshd logs the key ID
                           │   valid      = now-1m … max session+1m │
                           │   extensions = permit-pty only         │
                           │   source-address (optional)            │
                           └ pinned host key must match ───────────►│
```

1. The user opens `/hosts/{host}/ssh?action=ssh` (WebSocket, same frame protocol
   as exec).
2. `AccessService.Authorize` → `domain.Decide` with a host target. Denials are
   normal HTTP errors before the upgrade.
3. Recording segment 0 is written, then the `ssh.start` audit event. If either
   fails, nothing connects (**audit before action**, as in exec).
4. `sshaccess` generates an ephemeral ed25519 key in memory, signs a
   certificate for it, dials the host, checks the host key against the pinned
   one, authenticates, requests a PTY (`xterm-256color`) and a shell.
5. Output goes through the existing output hub to the owner, watchers and the
   recorder. Input and resizes are recorded.
6. The stream is registered in `StreamManager`, so it is re-evaluated every
   30 s and on every policy change. Expired grants, closed windows,
   break-glass ending, a disabled user or the end of the web session end it,
   with the same warnings and `closes_at` as exec. Idle timeout and maximum
   duration come from the existing exec settings.
7. On end, the channel and the connection are closed. The login shell receives
   SIGHUP. Processes started with `nohup`, `tmux` or `screen` survive, as with
   any agentless approach.

The key and certificate exist only in the portal's memory for the session.
They are worthless after the session: they expire a minute after the longest
possible session, and with the source restriction they work only from the
portal.

## Preparing a host

The host needs no agent and no global sshd change. Trust in the tiphys CA is
**per account**, through `cert-authority` lines in an `authorized_keys` file
that root owns:

```
# /etc/ssh/tiphys/authorized_keys/tiphys        (root:root 0644)
cert-authority,principals="tiphys",no-port-forwarding,no-agent-forwarding,no-X11-forwarding,no-user-rc[,from="10.0.0.0/8"] ssh-ed25519 AAAA… tiphys-ca

# /etc/ssh/tiphys/authorized_keys/tiphys-admin  (root:root 0644)
cert-authority,principals="tiphys-admin",no-port-forwarding,no-agent-forwarding,no-X11-forwarding,no-user-rc[,from="…"] ssh-ed25519 AAAA… tiphys-ca
```

And one `Match` block at the **end** of `/etc/ssh/sshd_config`:

```
Match User tiphys,tiphys-admin
    AuthorizedKeysFile /etc/ssh/tiphys/authorized_keys/%u
    AuthorizedPrincipalsFile none
    PasswordAuthentication no
    KbdInteractiveAuthentication no
    AllowTcpForwarding no
    AllowAgentForwarding no
    AllowStreamLocalForwarding no
    X11Forwarding no
    PermitTunnel no
```

Why this and not the global `TrustedUserCAKeys`:

- **It does not touch any existing setup.** If Teleport (agentless) or anything
  else already uses `TrustedUserCAKeys`, nothing changes for it. sshd accepts
  only one value of that option, so adding a second CA there would mean editing
  a file someone else manages.
- **The tiphys CA opens only these two accounts.** With `TrustedUserCAKeys` a
  certificate whose principal is `ubuntu` or `root` would be accepted for those
  accounts. Here a leaked CA key still reaches only `tiphys` and `tiphys-admin`.
- **Users cannot plant their own key.** The keys file lives outside the home
  directory and is owned by root, so a `tiphys` user cannot add a personal key
  to `~/.ssh/authorized_keys` to come back without the portal. `tiphys-admin`
  has root through sudo and can change anything; that is inherent to the role.

Accounts:

- `tiphys` and `tiphys-admin`: normal shell, password locked, no keys of their
  own.
- `tiphys-admin`: `ALL=(ALL) NOPASSWD: ALL` in `/etc/sudoers.d/tiphys`.
- `tiphys`: a list of allowed sudo commands, empty by default.

### Playbook

`deploy/ansible/tiphys-host/` is run on any host you want to add. It is
idempotent and supports `--check`. Variables:

| Variable | Default | Meaning |
|---|---|---|
| `tiphys_ca_public_keys` | required | CA public keys to trust (a list, so a rotation can trust two) |
| `tiphys_user` / `tiphys_admin_user` | `tiphys` / `tiphys-admin` | Account names (must match the portal) |
| `tiphys_user_sudo_commands` | `[]` | Commands `tiphys` may run with sudo |
| `tiphys_source_cidrs` | `[]` | Optional: adds `from="…"` to both lines |
| `tiphys_firewall` | `none` | Optional: `ufw` or `firewalld`, opens port 22 to `tiphys_source_cidrs` only |
| `tiphys_list_environment` / `tiphys_list_group` | empty | Only used in the printed list entry |

Steps:

1. Create the two accounts and the sudoers file (checked with `visudo -cf`).
2. Write the two keys files and the `Match` block (Ansible `blockinfile` with a
   marker). On OpenSSH older than 8.7 the block uses
   `ChallengeResponseAuthentication` instead of `KbdInteractiveAuthentication`.
3. Validate with `sshd -t`, then check the effective settings with
   `sshd -T -C user=tiphys,host=x,addr=<cidr address>` and the same for
   `tiphys-admin`. If anything differs from the expected values (for example an
   earlier `Match` block catches these users first), stop without reloading.
4. Reload sshd (`ssh` on Debian/Ubuntu, `sshd` on RHEL). Reload keeps open
   sessions.
5. Print the host's entry for the list, **including its ed25519 host key**, so
   it can be pasted into the Secret and no admin approval is needed.

## Registering hosts

### Host lists

Any Secret in the portal's namespace annotated `tiphys/hosts: <list name>` is a
list of hosts, in the `hosts.yaml` key:

```yaml
environment: prod        # default for the list's hosts (optional)
group: db                # default host group (optional)
hosts:
  - name: db-01          # DNS-1123 label, unique across all lists
    address: 10.0.4.11   # IP or DNS name
    hostKey: "ssh-ed25519 AAAA…"   # optional; pinned when present
  - name: db-02
    address: db-02.internal
    port: 2222           # default 22
    environment: staging # overrides the list default
    protected: true      # hidden from non-admin pickers, refused in requests
  # jump: bastion-01     # reserved for later: a host with jump is refused for now
```

- `HostService` follows the Secrets (watch + periodic sync) and keeps a `hosts`
  row per host, recording which list it came from. Changes from the lists are
  audited (`host.listed`, `host.updated`, `host.unlisted`) with before/after.
- A host whose name appears in two lists is unusable in both, with the error
  shown to admins. The portal never picks one.
- A host removed from every list keeps its row and its grants, shows as
  "Not listed" and is unusable. An admin may delete it once no grant references
  it (like a cluster).
- The portal **never writes Secrets**, so the lists stay with the operator.
- Environment comes only from the list (no UI override in phase 1). Hosts
  without an environment are `unlabeled`, like clusters.

### Host keys

- `hostKey` in the list: pinned as is.
- No `hostKey`: the first check reads the key and the host waits as
  "Host key awaiting approval". An admin compares the fingerprint
  (`ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub` on the host) and approves
  it (audited). Only then is the host usable.
- A changed key (reinstall, or an attack) makes the host unusable at once. The
  new fingerprint is shown; the list is updated or an admin approves the new
  key (audited). No connection is ever made to an unknown key.
- Host certificates (`@cert-authority`) can be added later as an alternative to
  pinning.

### Registration from the UI (local testing)

Like `CLUSTER_REGISTRATION_FROM_UI`, and with its own flag
`HOST_REGISTRATION_FROM_UI=true`. Config refuses it unless
`CREDENTIAL_STORE=dir` and `HOSTS_DIR` is set; production always reads
Secrets. When on:

- The admin endpoints to add, edit and delete hosts are registered, and the UI
  learns it from `me.settings.hostRegistration`.
- Hosts added in the UI are written to `HOSTS_DIR/ui.yaml`. Hosts from other
  files stay read-only in the UI, as a cluster with a Secret is today.
- As in `ClusterService.Add`, the row is created before the file is written,
  so the directory watch finds it. Delete removes the host from the file too.
- Every change is an `admin.change` with before/after.
- `hostKey` may be left empty; the approval flow then applies as usual.
- When `HOSTS_DIR` has no `ssh_ca` file, the CA card offers **Generate CA**,
  which writes a new ed25519 key there, so local testing needs no
  `ssh-keygen`. This exists only in this mode; in production the operator
  creates the CA (see below).

### The CA

One Secret annotated `tiphys/ssh-ca: "true"` with the private key in the `ca`
key (OpenSSH format, from `ssh-keygen -t ed25519 -N '' -f tiphys_ca`):

- No CA Secret, an unreadable one or more than one: SSH sessions are refused
  (`ssh_unavailable`) and the admin page says why. The portal never chooses
  between two CAs.
- The public key and its fingerprint are shown to admins only, on the Hosts
  section, to copy into the playbook's variables.
- Rotation: create a new key, run the playbook with **both** public keys,
  replace the key in the Secret, run the playbook with the new one only.
- The private key is the master key of every prepared host: whoever reads it
  can sign `tiphys-admin` certificates. Limit who can read Secrets in the
  portal's namespace. (Keeping it in Vault or a KMS is possible later: the
  signer is a `crypto.Signer` behind an interface.)

## Authorization (domain)

`domain` keeps one decision point; hosts become a second kind of target.

**Target.** `domain.Target` gains `Host string`. A host target has only `Host`;
a Kubernetes target never has it.

**Actions.**

```go
ActionSSH      Action = "ssh"       // shell as tiphys
ActionSSHAdmin Action = "ssh-admin" // shell as tiphys-admin
func (a Action) OnHost() bool
```

`ssh-admin` does not imply `ssh`; they are granted separately. Kubernetes
actions on a host target, or host actions on a Kubernetes target, are denied
(`ReasonInvalidTarget`), so no code path can mix them.

**Scopes.**

| Scope | Covers | Actions allowed |
|---|---|---|
| `workloads`, `namespace`, `cluster`, `argo_project` | as today | Kubernetes only |
| `host` (`Scope.Host`) | one host | host only |
| `host_group` (`Scope.HostGroup`) | every host of the group, also later ones | host only |
| `environment` | every namespace **and** every host of the environment | both |

`ValidateScopeActions` enforces the table. `Scope.Same` compares the new
fields, so approving a request merges into the same-scope grant by the
existing rule (`web/src/lib/requestMerge.ts` mirrors it).

Existing grants do not change meaning: none of them holds `ssh` or
`ssh-admin`, so an existing `environment` grant opens no host.

**Policy.** `PolicyInput` gains `Hosts` (name → environment, group,
protected). `Policy.Environment` returns the host's environment for a host
target. `grantCovers` gets the two new scopes and the host case of
`environment`. `GrantedHostActions(sub, host, now)` lists what a user holds on
a host, for the user's host list.

**Time windows.** Unchanged evaluation. Rules list their actions, so a rule
applies to SSH only when it lists `ssh`/`ssh-admin` (admins set this up
themselves). `global`, `environment` and `team` rules apply to hosts as they
are; `cluster` and `argo_project` rules never match a host; a new window scope
`host_group` matches a group's hosts. `closes_at` and the boundary scan work
unchanged.

**Holidays.** Unchanged: they close every action but `logs`, so `ssh` and
`ssh-admin` are closed on holidays of the host's environment unless
break-glass is active.

**Break-glass.** Unchanged, in all three modes, including scheduled starts.

**Roles.** Unchanged: users and auditors through grants, admins everywhere
(including `ssh-admin` on every host, as admins have exec everywhere today).

**Protected hosts.** Like protected clusters: not part of `Decide`; hidden
from non-admins' pickers and refused by `AccessRequestService.Create` (422).
Admins can grant on them.

**Access requests.** A request may name a `host` or `host_group` scope with
host actions, for the user or (by a manager) for a team. `environment`
remains admin only. Batch requests and admin batch grants over hosts come
later.

## Application

**Ports** (`ports.go`):

```go
// HostAccess is the sshaccess module as a port: the only way the portal
// reaches hosts. Verbs take a registered host name, never an address.
type HostAccess interface {
    // SetHost/RemoveHost keep the registry: address, port, pinned key, usable.
    SetHost(h HostEndpoint)
    RemoveHost(name string)
    // Shell signs a certificate for the login, connects and opens a PTY shell.
    Shell(ctx context.Context, actor HostActor, host string, login domain.HostLogin, cols, rows uint16) (ExecStream, error)
    // Ping does a handshake and verifies the host key (no authentication).
    Ping(ctx context.Context, host string) (HostPing, error)
    // Probe logs in and runs the access checks (see HostService).
    Probe(ctx context.Context, host string) (HostProbe, error)
}

type HostActor struct { Username, SessionID string }

// HostListStore reads the tiphys/hosts and tiphys/ssh-ca Secrets.
type HostListStore interface {
    Lists(ctx context.Context) ([]HostList, error)
    Watch(ctx context.Context, onChange func()) error
    CA(ctx context.Context) (crypto.Signer, error) // ErrNoCA, ErrManyCAs
}
```

`ExecStream` is reused as is.

**HostService** (like `ClusterService`, tick 5 s):

- Syncs rows from the lists, pushes endpoints to `HostAccess`, audits changes.
- Ping: every 30 s when healthy, every 10 s after a failure. Three failures in
  a row make the host unreachable; one success makes it reachable again. A
  host key mismatch makes it unusable at once.
- Access check every 5 minutes (and on demand from the admin page), as the
  equivalent of the cluster privilege check. It logs in as `tiphys` with key
  ID `tiphys:probe` and checks:
  1. `id -u` is not 0;
  2. `sudo -n true` fails (`tiphys` has no blanket sudo);
  3. opening a `direct-tcpip` channel (port forwarding) is refused;
  4. a certificate for principal `root` is refused.

  A host is unusable until the check passes; violations are shown to admins.
- `ApproveHostKey`, `Delete` through `AdminService.mutate` (audited in the same
  transaction).

**ExecService** opens host sessions too. A new `OpenHost(ctx, p,
HostShellRequest{Host, Action, Cols, Rows})` follows `Open` step by step
(authorize, recording header, `ssh.start` audit, session row, connect, stream,
hub, pump, supervise). Watch, Terminate, replay, `AbandonedSessions` and the
recording format need no change. Prod notifications use the same event with
the title "SSH opened in prod".

`domain.ExecSession` gains `Host` and `Login`. A host session has empty
`Cluster`, `Namespace` and `Pod`.

**Audit.** New event types `ssh.start` and `ssh.end` (watch, terminate and
replay keep their `exec.*` types, since they act on a session). `AuditEvent`
gains `Host`; details carry `login`, `address`, `certSerial` and
`hostKeyFingerprint`. The key ID in the host's `auth.log` (`tiphys:<user>:
<session>`) ties the host's own log to the audit trail.

## Infrastructure

`internal/infra/sshaccess` (new) is the only code with SSH clients and the CA,
like kubeaccess for Kubernetes. Before every call it checks that the host is
registered, its name valid, it is usable and its key is pinned. Then:

- the certificate as described above (serial random; source-address from
  `SSH_SOURCE_CIDRS` when set);
- `golang.org/x/crypto/ssh` client with its default modern algorithms, a
  10 s dial and handshake timeout, `HostKeyCallback` that accepts only the
  pinned key;
- only a `session` channel with `pty-req` and `shell`; no agent, no X11, no
  environment variables, every channel the server opens is rejected;
- `window-change` on resize, `keepalive@openssh.com` every 30 s (three misses
  end the session).

`internal/infra/credentials` gets the Secret and directory implementations of
`HostListStore` (directory mode for local testing: `*.yaml` lists and a
`ssh_ca` file in `HOSTS_DIR`, refused unless `CREDENTIAL_STORE=dir`). Only the
directory store implements writing (`PutHost`, `DeleteHost`, `GenerateCA`, for
`HOST_REGISTRATION_FROM_UI`); the Secret store returns `ErrReadOnly`, as
`SecretStore.Put` does. The portal's Role is unchanged (get, list, watch on
Secrets).

## Database (migration 13)

- `hosts`: `id`, `name` (unique), `address`, `port`, `environment`,
  `host_group`, `protected`, `list_secret`, `listed`, `host_key`,
  `host_key_source` (`list`, `approved`), `seen_host_key`, `reachable`,
  `last_check_at`, `access_check_ok`, `violations`, `error`, `created_at`,
  `updated_at`, `updated_by`.
- `grants` and `access_requests`: `host_id` (`REFERENCES hosts ON DELETE
  RESTRICT`, like `cluster_id`) and `host_group`; the `scope_kind`, scope and
  action CHECKs widened to the new kinds and actions.
- `time_windows`: scope kind `host_group`; actions CHECK widened.
- `exec_sessions`: `host`, `login` (`NOT NULL DEFAULT ''`; `cluster`,
  `namespace`, `pod` stay `NOT NULL` and are `''` for host sessions).
- `audit_events`: `host` with an index.

**Compatibility with the previous release** (it keeps running during the
rollout): its repositories cast scope kinds and actions without validating
them, and `grantCovers`/`windowApplies` return false for kinds they do not
know, so host grants and `host_group` rules grant nothing on an old pod. Its
lists show host grants and sessions with empty cluster columns. Nothing it
writes conflicts with the new columns. The feature is off by default
(`SSH_HOSTS_ENABLED`), so the new release changes nothing until it is turned
on.

## HTTP API

User (grants decide; admins see all):

- `GET /api/v1/hosts`: hosts the caller holds `ssh`/`ssh-admin` on, with
  environment, group, reachable, and the decision for both actions
  (`allowed`, `reason`, `closesAt`).
- `GET /api/v1/hosts/{host}`: the same for one host.
- `GET /api/v1/hosts/{host}/ssh?action=ssh|ssh-admin&cols=&rows=&csrf=`:
  WebSocket, same frames and control messages as exec.
- `GET /api/v1/catalog/hosts` and `/catalog/host-groups`: for grant and request
  forms (protected hosts hidden from non-admins).

Admin:

- `GET /api/v1/admin/hosts`: every host with list, status, key state,
  violations and list errors.
- `GET /api/v1/admin/ssh-ca`: public key, fingerprint, state.
- `POST /api/v1/admin/hosts/{host}/host-key/approve` `{fingerprint}`: must match
  the key last seen.
- `POST /api/v1/admin/hosts/{host}/check`: run the access check now.
- `DELETE /api/v1/admin/hosts/{host}`: only when not listed and no grant uses
  it; with `HOST_REGISTRATION_FROM_UI`, a host from `ui.yaml` is removed from
  the file too.
- Only with `HOST_REGISTRATION_FROM_UI`: `POST /api/v1/admin/hosts` and
  `PUT /api/v1/admin/hosts/{host}` (name, address, port, environment, group,
  protected, hostKey; `ui.yaml` hosts only), and
  `POST /api/v1/admin/ssh-ca/generate` (409 when a CA exists).

Staff session endpoints are unchanged; their DTOs gain `host` and `login`.
`me.settings.sshEnabled` tells the UI whether the feature is on. Every
endpoint goes into `docs/api.md`.

## Frontend

- **Clusters & environments → Hosts** (admin only): the CA card (public key
  with copy button, fingerprint, state); the hosts table (name, address,
  environment, group, list, reachable, access check, key state) with Approve
  key, Check now and Delete; list errors such as duplicate names. With
  `hostRegistration`: Add host, Edit (for `ui.yaml` hosts) and Generate CA.
- **Hosts** page for everyone with access: their hosts with Connect
  (`ssh` / `ssh-admin`, each shown only when allowed) and the closing time.
- **Terminal**: the exec terminal component reused on `/hosts/:host/ssh`.
- **Grants, access requests, time windows**: a target type switch
  (Kubernetes / Hosts) that offers the matching scopes and only the actions
  allowed for the scope.
- **Sessions, replay, watch, audit log**: show `login@host` for host
  sessions; audit filter by host.
- Mock data for `VITE_MOCK=1`.

## Configuration

| Variable | Default | Meaning |
|---|---|---|
| `SSH_HOSTS_ENABLED` | `false` | Turns the feature on (routes, Secret watch, UI) |
| `SSH_SOURCE_CIDRS` | empty | Optional `source-address` for every certificate |
| `HOSTS_DIR` | empty | Directory store for local testing (`CREDENTIAL_STORE=dir` only) |
| `HOST_REGISTRATION_FROM_UI` | `false` | Add/edit/delete hosts and generate the CA in the UI (needs `CREDENTIAL_STORE=dir` and `HOSTS_DIR`) |

Account names are fixed to `tiphys` and `tiphys-admin` in phase 1, the
playbook defaults.

The portal chart gets the variables and, if NetworkPolicies are used,
egress to TCP 22 of the host CIDRs (value `ssh.egressCIDRs`).
`deploy/portal/portal.yaml` follows the chart. `.env.example` documents
everything.

## Limits worth knowing

- Recording is the terminal stream. Everything shown on the terminal is
  recorded (including `sudo` and editors). What a process does without
  printing is not. Enhanced (kernel-level) recording needs an agent.
- `tiphys-admin` is root. It can change the host, including removing tiphys's
  trust. The portal records it but cannot limit it.
- `ssh` from a host to another host is recorded only as terminal output and
  leaves the portal's control. Preventing it is a host or network matter.
- Background processes survive the end of a session.
- One replica and rolling updates: a restart ends open SSH sessions, as it
  ends exec sessions today.

## Testing

- `domain`: host scopes, shared environment, action/target mixing, windows with
  `host_group`, holidays, break-glass, `closes_at` for host targets.
- `sshaccess`: an in-process `x/crypto/ssh` server for certificates, host key
  pinning, rejected channels, resize and keepalive.
- Integration (opt-in, like the Kubernetes one): an OpenSSH container prepared
  **by the playbook itself**, then sessions, recordings and the access check
  against it, including the negative cases (wrong host key, root principal,
  port forwarding, expired certificate).
- Frontend tests for the forms and `requestMerge.ts`; postgres tests for the
  migration and repositories.

## Implementation order

Each step is built, tested and committed on its own:

1. Domain (target, actions, scopes, windows) and its tests.
2. Migration 13 and repositories.
3. `sshaccess`, the list and CA stores, `HostService`.
4. Host sessions in `ExecService`, API and WebSocket.
5. Frontend.
6. Playbook, chart, `.env.example`, `spec.md`, `docs/api.md`,
   `docs/database.md`, `CLAUDE.md`.

## Later

- **Jump hosts:** the reserved `jump` field; `sshaccess` dials through the jump
  host with its own certificate.
- **File transfer** (Kubernetes and hosts): separate `file-download` and
  `file-upload` actions, one `FileTransferService`, a copy of each file in
  MinIO (`transfers/…`, Object Lock) and its hash in the audit; on hosts over
  the `sftp` subsystem.
- **Typed verbs without a shell:** `logs` (journal of allow-listed units),
  `restart` (allow-listed units), `run-command` (admin-defined runbooks), as a
  third account with narrow sudoers.
- **CLI:** an SSH-terminating proxy in the portal reached through `tiphys proxy`
  as `ProxyCommand`, with a second CA for user certificates.
- **Host certificates** as an alternative to pinned host keys; CA in Vault or a
  KMS.
