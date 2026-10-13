package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/aahoughton/agora/internal/core"
)

// Accounts, tokens and memberships. Every call is authorized in core: server
// admins manage accounts, an account manages its own tokens, and namespace
// admins manage their namespace's members. Each change is logged once, with
// the acting account, the action and its target; secrets never are.

func (h *handler) audit(r *http.Request, p core.Principal, action, target string, attrs ...any) {
	h.logger.InfoContext(r.Context(), "admin", append([]any{"actor", p.AccountID, "action", action, "target", target}, attrs...)...)
}

func (h *handler) whoami(w http.ResponseWriter, r *http.Request, p core.Principal) error {
	id, err := h.svc.Whoami(r.Context(), p)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"auth_mode": h.authMode, "version": h.version, "account": id.Account, "memberships": id.Memberships,
	})
	return nil
}

func (h *handler) listAccounts(w http.ResponseWriter, r *http.Request, p core.Principal) error {
	as, err := h.svc.ListAccounts(r.Context(), p)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"accounts": as})
	return nil
}

func (h *handler) createAccount(w http.ResponseWriter, r *http.Request, p core.Principal) error {
	var req struct {
		Name        string `json:"name"`
		ServerAdmin bool   `json:"server_admin"`
	}
	if err := h.decode(w, r, &req); err != nil {
		return err
	}
	a, err := h.svc.CreateAccount(r.Context(), p, req.Name, req.ServerAdmin)
	if err != nil {
		return err
	}
	h.audit(r, p, "create_account", a.Name, "server_admin", a.ServerAdmin)
	writeJSON(w, http.StatusCreated, a)
	return nil
}

func (h *handler) updateAccount(w http.ResponseWriter, r *http.Request, p core.Principal) error {
	var req struct {
		Disabled    *bool `json:"disabled"`
		ServerAdmin *bool `json:"server_admin"`
	}
	if err := h.decode(w, r, &req); err != nil {
		return err
	}
	a, err := h.svc.UpdateAccount(r.Context(), p, r.PathValue("account"),
		core.AccountUpdate{Disabled: req.Disabled, ServerAdmin: req.ServerAdmin})
	if err != nil {
		return err
	}
	h.audit(r, p, "update_account", a.Name, "disabled", a.Disabled, "server_admin", a.ServerAdmin)
	writeJSON(w, http.StatusOK, a)
	return nil
}

func (h *handler) listTokens(w http.ResponseWriter, r *http.Request, p core.Principal) error {
	ts, err := h.svc.ListTokens(r.Context(), p, r.PathValue("account"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"tokens": ts})
	return nil
}

func (h *handler) issueToken(w http.ResponseWriter, r *http.Request, p core.Principal) error {
	var req struct {
		Label      string `json:"label"`
		TTLSeconds int64  `json:"ttl_seconds"`
	}
	if err := h.decode(w, r, &req); err != nil {
		return err
	}
	if req.TTLSeconds < 0 || req.TTLSeconds > maxTokenTTLSeconds {
		return invalid("ttl_seconds must be between 0 (never expires) and %d", maxTokenTTLSeconds)
	}
	t, err := h.svc.IssueToken(r.Context(), p, r.PathValue("account"), req.Label, time.Duration(req.TTLSeconds)*time.Second)
	if err != nil {
		return err
	}
	h.audit(r, p, "issue_token", t.Account, "token", t.ID)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, t)
	return nil
}

// maxTokenTTLSeconds is the longest token lifetime, 100 years.
const maxTokenTTLSeconds = 100 * 366 * 24 * 3600

func (h *handler) revokeAllTokens(w http.ResponseWriter, r *http.Request, p core.Principal) error {
	n, err := h.svc.RevokeAllTokens(r.Context(), p, r.PathValue("account"))
	if err != nil {
		return err
	}
	h.audit(r, p, "revoke_all_tokens", r.PathValue("account"), "revoked", n)
	writeJSON(w, http.StatusOK, map[string]any{"revoked": n})
	return nil
}

func (h *handler) revokeToken(w http.ResponseWriter, r *http.Request, p core.Principal) error {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		return invalid("token id must be a positive integer")
	}
	if err := h.svc.RevokeToken(r.Context(), p, id); err != nil {
		return err
	}
	h.audit(r, p, "revoke_token", strconv.FormatInt(id, 10))
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (h *handler) listMembers(w http.ResponseWriter, r *http.Request, p core.Principal) error {
	ms, err := h.svc.ListMemberships(r.Context(), p, r.PathValue("ns"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"members": ms})
	return nil
}

func (h *handler) setMember(w http.ResponseWriter, r *http.Request, p core.Principal) error {
	var req struct {
		Role core.Role `json:"role"`
	}
	if err := h.decode(w, r, &req); err != nil {
		return err
	}
	m, err := h.svc.SetMembership(r.Context(), p, r.PathValue("ns"), r.PathValue("account"), req.Role)
	if err != nil {
		return err
	}
	h.audit(r, p, "set_member", m.Account, "namespace", m.Namespace, "role", m.Role)
	writeJSON(w, http.StatusOK, m)
	return nil
}

func (h *handler) removeMember(w http.ResponseWriter, r *http.Request, p core.Principal) error {
	if err := h.svc.RemoveMembership(r.Context(), p, r.PathValue("ns"), r.PathValue("account")); err != nil {
		return err
	}
	h.audit(r, p, "remove_member", r.PathValue("account"), "namespace", r.PathValue("ns"))
	w.WriteHeader(http.StatusNoContent)
	return nil
}
