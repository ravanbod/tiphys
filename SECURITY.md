# Security policy

Tiphys brokers access to Kubernetes clusters, so a flaw in it can expose
clusters. Please report vulnerabilities privately, not in public issues.

## Reporting

Use GitHub's **Report a vulnerability** button on this repository's Security
tab (private vulnerability reporting). Include what is affected, how to
reproduce it, and the impact you expect. You should get a first answer within
a week; please give us a reasonable time to fix the problem before you
disclose it.

## What counts

Anything that breaks the guarantees the portal is built on, for example:

- getting access that `Decide` should deny (scope, action, time window,
  protected namespace, role), or keeping it after a revocation;
- making the portal send the cluster a request its typed verbs should not
  allow (another pod, namespace, object, path, body or selector);
- obtaining a credential that works against a cluster or Argo CD;
- syncing an Argo CD application the user may not sync, or seeing Secret
  contents;
- acting without an audit event, or altering or deleting audit events or exec
  recordings;
- getting past the chart's RBAC or admission policies as `tiphys:user`;
- CSRF, session fixation, XSS or WebSocket origin bypasses in the web app.

Bugs in the deployment around Tiphys (Keycloak, Argo CD, Kubernetes, your
Ingress) are out of scope unless Tiphys makes them exploitable.

## Supported versions

Fixes go into `main`; there are no maintained release branches yet.
