package server

import (
	"context"
	"database/sql"
	"errors"

	"tunnelx/internal/adminapi"
	"tunnelx/internal/policy"
	"tunnelx/internal/session"
	"tunnelx/internal/store"
)

func accountBackendError(err error) error {
	switch {
	case errors.Is(err, store.ErrAccountConflict), errors.Is(err, store.ErrIdentityConflict):
		return adminapi.ErrConflict
	case errors.Is(err, store.ErrAccountInvalid):
		return &adminapi.BackendError{Code: "invalid_account", Message: "账号需为3–64位小写字母、数字或._-；密码需为15–128个字符，设备上限1–50"}
	case errors.Is(err, store.ErrAccountLimit):
		return &adminapi.BackendError{Code: "account_limit", Message: "账号或设备数量达到上限"}
	case errors.Is(err, sql.ErrNoRows), errors.Is(err, store.ErrAccountAuth):
		return adminapi.ErrNotFound
	default:
		return err
	}
}
func (b *adminBackend) ListAccounts(ctx context.Context) ([]store.Account, error) {
	return (*Server)(b).store.ListAccounts(ctx)
}
func (b *adminBackend) ListAccountDevices(ctx context.Context, username string) ([]store.AccountDevice, error) {
	v, err := (*Server)(b).store.ListAccountDevices(ctx, username)
	return v, accountBackendError(err)
}
func (b *adminBackend) CreateAccount(ctx context.Context, r adminapi.CreateAccountRequest) (store.Account, error) {
	s := (*Server)(b)
	// Creating a fresh account has no live devices to invalidate.
	account, err := s.store.CreateAccountWithRole(ctx, r.Username, r.Password, r.MaxDevices, r.IsAdmin, s.action(ctx, r.Username, "create_account", r.Reason, "success", ""))
	if err == nil {
		s.notifyAudit(0)
		if s.adminAPI != nil {
			s.adminAPI.Publish("clients.changed", nil)
		}
	}
	return account, accountBackendError(err)
}
func (b *adminBackend) UpdateAccount(ctx context.Context, username string, r adminapi.UpdateAccountRequest) (store.Account, error) {
	s := (*Server)(b)
	var account store.Account
	var fingerprints []string
	var removals []*session.Removal
	err := s.policy.PersistChange("", func() error {
		var err error
		// Read after acquiring the management writer: a simultaneous enrollment
		// must be either included here or rejected against the new account epoch.
		fingerprints, err = s.store.AccountFingerprints(ctx, username)
		if err != nil {
			return err
		}
		account, err = s.store.UpdateAccountWithRole(ctx, username, r.Password, r.Enabled, r.IsAdmin, r.ExpectedGeneration, s.action(ctx, username, "update_account", r.Reason, "success", ""))
		return err
	}, func(_ *policy.Snapshot) {
		for _, fp := range fingerprints {
			removals = append(removals, s.sessions.RemoveFingerprint(fp, "account_updated")...)
		}
	})
	if errors.Is(err, store.ErrCommitOutcomeUnknown) {
		for _, fp := range fingerprints {
			removals = append(removals, s.sessions.RemoveFingerprint(fp, "authorization_commit_uncertain")...)
		}
	}
	for _, removal := range removals {
		removal.Close()
	}
	if err == nil {
		s.notifyAudit(0)
		if s.adminAPI != nil {
			s.adminAPI.Publish("clients.changed", nil)
		}
	}
	return account, accountBackendError(err)
}
