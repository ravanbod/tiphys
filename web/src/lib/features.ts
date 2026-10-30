/**
 * The Argo CD integration is switched off in the panels for now: no Argo app
 * view or project filters on Workloads, no "Argo project" scope when granting,
 * requesting, overriding environments or writing time rules, and no stale-data
 * banner. The server still supports it. Set this to true to bring all of it
 * back. Always shown: the Argo CD cache card on Clusters & environments and the
 * Argo apps on the namespace page.
 */
export const ARGO_UI = false as boolean;
