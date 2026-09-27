package server

import (
	"context"
	"database/sql"
	"errors"

	"tunnelx/internal/adminapi"
	"tunnelx/internal/store"
)

func accountBackendError(err error) error {
	switch {
	case errors.Is(err, store.ErrAccountConflict), errors.Is(err, store.ErrIdentityConflict):
		return adminapi.ErrConflict
	case errors.Is(err, store.ErrAccountInvalid):
		return &adminapi.BackendError{Code: "invalid_account", Message: "账号需为3–64位小写字母、数字或._-；密码需为15–128个字符"}
	case errors.Is(err, store.ErrAccountLimit):
		return &adminapi.BackendError{Code: "account_limit", Message: "账号数量达到上限"}
	case errors.Is(err, sql.ErrNoRows), errors.Is(err, store.ErrAccountAuth):
		return adminapi.ErrNotFound
	default:
		return err
	}
}
func (b *adminBackend) ListAccounts(ctx context.Context) ([]store.Account, error) {
	return (*Server)(b).store.ListAccounts(ctx)
}
func (b *adminBackend) CreateAccount(ctx context.Context, r adminapi.CreateAccountRequest) (store.Account, error) {
	s := (*Server)(b)
	// Creating a fresh account has no live devices to invalidate.
	account, err := s.store.CreateAccountWithRole(ctx, r.Username, r.Password, store.DefaultMaxAccountDevices, true, s.action(ctx, r.Username, "create_account", r.Reason, "success", ""))
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
	account, err := s.store.UpdateAccount(ctx, username, r.Password, r.Enabled, r.ExpectedGeneration, s.action(ctx, username, "update_account", r.Reason, "success", ""))
	if err == nil {
		s.notifyAudit(0)
		if s.adminAPI != nil {
			s.adminAPI.Publish("clients.changed", nil)
		}
	}
	return account, accountBackendError(err)
}
