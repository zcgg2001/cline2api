package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

const maxAccountSelection = 1000

// POST /admin/api/accounts/{disable,enable}, body: { accountIds: [...] }.
func handleAdminAccountDisable(w http.ResponseWriter, r *http.Request) {
	setSelectedAccountsDisabled(w, r, true)
}

func handleAdminAccountEnable(w http.ResponseWriter, r *http.Request) {
	setSelectedAccountsDisabled(w, r, false)
}

func setSelectedAccountsDisabled(w http.ResponseWriter, r *http.Request, disabled bool) {
	if r.Method != http.MethodPost {
		writeAPI(w, http.StatusMethodNotAllowed, apiResponse{Error: tAPI(r, "method_not_allowed")})
		return
	}
	ids, err := readAccountSelection(w, r)
	if err != nil {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: err.Error()})
		return
	}
	p := loadPool()
	poolMu.Lock()
	defer poolMu.Unlock()
	accounts, err := selectedAccountsLocked(p, ids)
	if err != nil {
		writeAPI(w, http.StatusNotFound, apiResponse{Error: err.Error()})
		return
	}
	previous := make([]bool, len(accounts))
	for i, a := range accounts {
		previous[i] = a.Disabled
		a.Disabled = disabled
	}
	if err := savePoolLocked(); err != nil {
		for i, a := range accounts {
			a.Disabled = previous[i]
		}
		writeAPI(w, http.StatusInternalServerError, apiResponse{Error: tAPI(r, "account_save_failed")})
		return
	}
	writeAPI(w, http.StatusOK, apiResponse{Success: true, Data: map[string]any{"updated": len(accounts), "disabled": disabled}})
}

func readAccountSelection(w http.ResponseWriter, r *http.Request) ([]string, error) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req struct {
		AccountIDs []string `json:"accountIds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return nil, fmt.Errorf("invalid JSON")
	}
	if len(req.AccountIDs) == 0 || len(req.AccountIDs) > maxAccountSelection {
		return nil, fmt.Errorf("select between 1 and %d accounts", maxAccountSelection)
	}
	seen := map[string]bool{}
	ids := make([]string, 0, len(req.AccountIDs))
	for _, id := range req.AccountIDs {
		if strings.TrimSpace(id) == "" {
			return nil, fmt.Errorf("account ID cannot be empty")
		}
		if !seen[id] {
			ids = append(ids, id)
			seen[id] = true
		}
	}
	return ids, nil
}

// Caller holds poolMu. Resolve every target before making changes or exporting.
func selectedAccountsLocked(p *AccountPool, ids []string) ([]*Account, error) {
	byID := make(map[string]*Account, len(p.Accounts))
	for _, a := range p.Accounts {
		byID[a.AccountID] = a
	}
	selected := make([]*Account, 0, len(ids))
	for _, id := range ids {
		a := byID[id]
		if a == nil {
			return nil, fmt.Errorf("account not found: %s", id)
		}
		selected = append(selected, a)
	}
	return selected, nil
}

func handleAdminBatchDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPI(w, 405, apiResponse{Error: tAPI(r, "method_not_allowed")})
		return
	}
	ids, err := readAccountSelection(w, r)
	if err != nil {
		writeAPI(w, 400, apiResponse{Error: err.Error()})
		return
	}
	p := loadPool()
	poolMu.Lock()
	defer poolMu.Unlock()
	if _, err := selectedAccountsLocked(p, ids); err != nil {
		writeAPI(w, 404, apiResponse{Error: err.Error()})
		return
	}
	selected := map[string]bool{}
	for _, id := range ids {
		selected[id] = true
	}
	oldAccounts, oldIndex, oldGroups := p.Accounts, p.CurrentIdx, p.GroupIndexes
	remaining := make([]*Account, 0, len(p.Accounts)-len(ids))
	for _, a := range p.Accounts {
		if !selected[a.AccountID] {
			remaining = append(remaining, a)
		}
	}
	p.Accounts, p.CurrentIdx, p.GroupIndexes = remaining, 0, map[string]int{}
	if err := savePoolLocked(); err != nil {
		p.Accounts, p.CurrentIdx, p.GroupIndexes = oldAccounts, oldIndex, oldGroups
		writeAPI(w, 500, apiResponse{Error: tAPI(r, "account_save_failed")})
		return
	}
	writeAPI(w, 200, apiResponse{Success: true, Data: map[string]any{"deleted": len(ids)}})
}
