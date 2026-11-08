package application

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"

	"tiphys/internal/domain"
)

// MaxBatchNamespaces caps a batch, so a batch cannot stand in for a whole
// cluster, which only an admin grants.
const MaxBatchNamespaces = 20

// CreateBatchRequest is the input of CreateBatch: the same actions,
// duration and reason on several namespaces of one cluster.
type CreateBatchRequest struct {
	Cluster       string
	Namespaces    []string
	Actions       []domain.Action
	DurationHours *int
	Reason        string
	// TeamID asks for a team the principal manages instead of for themself.
	TeamID int64
}

// CreateBatch files one namespace request per namespace, sharing a BatchID,
// in one transaction: all of them or, when any namespace is invalid or
// protected, none (the error names it). One notification announces the batch.
func (s *AccessRequestService) CreateBatch(ctx context.Context, p Principal, in CreateBatchRequest) ([]domain.AccessRequest, error) {
	if err := RequireRole(p, domain.RoleUser, domain.RoleAuditor); err != nil {
		return nil, err
	}
	var nss []string
	for _, n := range in.Namespaces {
		if n = strings.TrimSpace(n); n != "" && !slices.Contains(nss, n) {
			nss = append(nss, n)
		}
	}
	if len(nss) < 2 {
		return nil, Invalid("namespaces", "pick at least two namespaces (one is an ordinary request)")
	}
	if len(nss) > MaxBatchNamespaces {
		return nil, Invalid("namespaces", fmt.Sprintf("a batch has at most %d namespaces", MaxBatchNamespaces))
	}
	snap := s.policy.Current()
	if in.Cluster != "" && snap.Clusters[in.Cluster].Protected {
		return nil, Invalid("cluster", "this cluster is protected and cannot be requested")
	}
	one := func(ns string) CreateRequest {
		return CreateRequest{
			Scope:   domain.Scope{Kind: domain.ScopeNamespace, Cluster: in.Cluster, Namespace: ns},
			Actions: in.Actions, DurationHours: in.DurationHours, Reason: in.Reason, TeamID: in.TeamID,
		}
	}
	for _, ns := range nss {
		c := one(ns)
		if err := c.Scope.Validate(); err != nil {
			return nil, Invalid("namespaces", fmt.Sprintf("%s: %v", ns, err))
		}
		if snap.Policy.IsProtected(ns) {
			return nil, Invalid("namespaces", ns+" is protected and cannot be requested")
		}
		if err := validateRequest(c); err != nil {
			return nil, err
		}
	}

	batch := uuid.NewString()
	reason := strings.TrimSpace(in.Reason)
	var out []domain.AccessRequest
	var evs []domain.AuditEvent
	err := s.store.InTx(ctx, func(r Repositories) error {
		out, evs = nil, nil
		if in.TeamID != 0 {
			if _, err := requireTeamManager(ctx, r, p, in.TeamID); err != nil {
				return err
			}
		}
		for _, ns := range nss {
			c := one(ns)
			req, err := r.Requests.Create(ctx, domain.AccessRequest{
				UserID: p.User.ID, Username: p.User.Username, TeamID: in.TeamID, Scope: c.Scope, Actions: in.Actions,
				DurationHours: in.DurationHours, Reason: reason, Status: domain.RequestPending, BatchID: batch,
			})
			if err != nil {
				return err
			}
			e := s.audit.Event(p, domain.EventRequestCreate)
			e.Result, e.Reason = domain.ResultSucceeded, req.Reason
			e.Cluster, e.Namespace = c.Scope.Cluster, c.Scope.Namespace
			e.Details = map[string]any{"requestId": req.ID, "scope": c.Scope, "actions": in.Actions, "durationHours": in.DurationHours,
				"batchId": batch, "batchSize": len(nss)}
			if req.TeamID != 0 {
				e.Details["teamId"], e.Details["team"] = req.TeamID, req.TeamName
			}
			ev, err := s.audit.RecordIn(ctx, r, e)
			if err != nil {
				return err
			}
			out, evs = append(out, req), append(evs, ev)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, ev := range evs {
		s.audit.Publish(ev)
	}
	s.notify.Notify(NotificationMessage{
		Event: domain.NotifyRequestCreated, Title: fmt.Sprintf("Access request created (%d namespaces)", len(out)),
		Fields: append([]NotificationField{{Name: "Who", Value: p.User.Username}}, batchFields(out, out)...),
		Link:   s.notify.Link("/admin/requests"),
	})
	return out, nil
}

// BatchDecision is an admin's decision on a batch: the requests in Approve
// are approved with the options, every other one is rejected; Note goes on
// all of them.
type BatchDecision struct {
	Approve []int64
	ApproveOptions
}

// DecideBatch decides every request of a batch in one transaction: all or,
// when any is no longer pending or cannot be approved, none (the error
// names it).
func (s *AccessRequestService) DecideBatch(ctx context.Context, p Principal, batchID string, d BatchDecision) ([]domain.AccessRequest, error) {
	if err := RequireRole(p, domain.RoleAdmin); err != nil {
		return nil, err
	}
	now := s.clock.Now()
	var out []domain.AccessRequest
	var evs []domain.AuditEvent
	err := s.store.InTx(ctx, func(r Repositories) error {
		out, evs = nil, nil
		reqs, err := r.Requests.ListBatch(ctx, batchID)
		if err != nil {
			return err
		}
		for _, id := range d.Approve {
			if !slices.ContainsFunc(reqs, func(x domain.AccessRequest) bool { return x.ID == id }) {
				return Invalid("approve", fmt.Sprintf("request #%d is not part of this batch", id))
			}
		}
		for _, req := range reqs {
			if req.Status != domain.RequestPending {
				return fmt.Errorf("%w: request #%d (%s) is already %s", ErrConflict, req.ID, req.Scope.Namespace, req.Status)
			}
		}
		for _, req := range reqs {
			var ev domain.AuditEvent
			if slices.Contains(d.Approve, req.ID) {
				req, ev, _, err = s.approveIn(ctx, r, p, req, d.ApproveOptions, now)
			} else {
				req, ev, err = s.rejectIn(ctx, r, p, req, d.Note, now)
			}
			if err != nil {
				return inNamespace(req.Scope.Namespace, err)
			}
			out, evs = append(out, req), append(evs, ev)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, ev := range evs {
		s.audit.Publish(ev)
	}
	var approved []domain.AccessRequest
	for _, r := range out {
		if r.Status == domain.RequestApproved {
			approved = append(approved, r)
		}
	}
	verdict := "rejected"
	if len(approved) > 0 {
		s.policy.Changed(ctx)
		verdict = fmt.Sprintf("approved %d of %d", len(approved), len(out))
	}
	s.notifyBatchDecided(out, approved, verdict)
	return out, nil
}

// batchFields describe a batch for notifications: the namespaces in shown
// (all, or the approved ones) and what was asked.
func batchFields(all, shown []domain.AccessRequest) []NotificationField {
	first := all[0]
	if len(shown) > 0 {
		first = shown[0]
	}
	nss := make([]string, len(shown))
	for i, r := range shown {
		nss[i] = r.Scope.Namespace
	}
	fields := requestFields(first)
	for i, f := range fields {
		if f.Name == "What" {
			acts := make([]string, len(first.Actions))
			for j, a := range first.Actions {
				acts[j] = string(a)
			}
			fields[i].Value = fmt.Sprintf("%d namespaces on %s: %s · %s", len(shown), first.Scope.Cluster, strings.Join(nss, ", "), strings.Join(acts, ", "))
		}
	}
	return fields
}

func (s *AccessRequestService) notifyBatchDecided(all, approved []domain.AccessRequest, verdict string) {
	r := all[0]
	fields := []NotificationField{{Name: "Requester", Value: r.Username}, {Name: "Decision", Value: verdict + " by " + r.DecidedBy}}
	if len(approved) > 0 {
		fields = append(fields, batchFields(all, approved)...)
	} else {
		fields = append(fields, batchFields(all, all)...)
	}
	if r.DecisionNote != "" {
		fields = append(fields, NotificationField{Name: "Note", Value: r.DecisionNote})
	}
	s.notify.Notify(NotificationMessage{
		Event: domain.NotifyRequestDecided, Title: "Access request " + verdict, Fields: fields,
		Link: s.notify.Link("/my-access"), DirectTo: r.Username,
	})
}

// countRequests counts pending requests as the admin sees them: a batch once.
func countRequests(reqs []domain.AccessRequest) int {
	n := 0
	seen := map[string]bool{}
	for _, r := range reqs {
		if r.BatchID == "" {
			n++
		} else if !seen[r.BatchID] {
			seen[r.BatchID] = true
			n++
		}
	}
	return n
}

// inNamespace names the namespace in an error of one request of a batch,
// also in a validation error's message (which is what the API shows).
func inNamespace(ns string, err error) error {
	var inv *InvalidError
	if errors.As(err, &inv) {
		out := &InvalidError{Message: ns + ": " + inv.Message, Fields: map[string]string{}}
		for k, v := range inv.Fields {
			out.Fields[k] = ns + ": " + v
		}
		return out
	}
	return fmt.Errorf("%s: %w", ns, err)
}
