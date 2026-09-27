package store

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
)

func TestConcurrentAdministratorDemotionsPreserveOneEnabledAdministrator(t *testing.T) {
	s := registrationStore(t)
	ctx := context.Background()
	for _, username := range []string{"first-admin", "second-admin"} {
		if _, err := s.CreateAccountWithRole(ctx, username, accountTestPassword, 10, true, AdminAction{}); err != nil {
			t.Fatal(err)
		}
	}
	start := make(chan struct{})
	errorsCh := make(chan error, 2)
	var done sync.WaitGroup
	for _, username := range []string{"first-admin", "second-admin"} {
		done.Add(1)
		go func() {
			defer done.Done()
			<-start
			isAdmin := false
			_, err := s.UpdateAccountWithRole(ctx, username, "", nil, &isAdmin, 1, AdminAction{})
			errorsCh <- err
		}()
	}
	close(start)
	done.Wait()
	close(errorsCh)
	var succeeded, rejected int
	for err := range errorsCh {
		if err == nil {
			succeeded++
		} else if errors.Is(err, ErrAccountConflict) {
			rejected++
		} else {
			t.Fatal(err)
		}
	}
	if succeeded != 1 || rejected != 1 {
		t.Fatalf("concurrent demotions: succeeded=%d rejected=%d", succeeded, rejected)
	}
	accounts, err := s.ListAccounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var enabledAdmins int
	for _, account := range accounts {
		if account.Enabled && account.IsAdmin {
			enabledAdmins++
		}
	}
	if enabledAdmins != 1 {
		t.Fatalf("enabled administrators remaining: %d", enabledAdmins)
	}
}

func TestAdminAccountRolesAndLastEnabledAdministrator(t *testing.T) {
	s := registrationStore(t)
	ctx := context.Background()
	ordinary := createTestAccount(t, s, "ordinary", 10)
	if ordinary.IsAdmin {
		t.Fatal("ordinary account acquired administrator access")
	}
	admin, err := s.CreateAccountWithRole(ctx, "admin", accountTestPassword, 10, true, AdminAction{})
	if err != nil || !admin.IsAdmin {
		t.Fatalf("create administrator: %+v %v", admin, err)
	}
	disabled, notAdmin := false, false
	for _, change := range []struct{ enabled, role *bool }{{&disabled, nil}, {nil, &notAdmin}, {&disabled, &notAdmin}} {
		if _, err := s.UpdateAccountWithRole(ctx, admin.Username, "replacement account password", change.enabled, change.role, admin.Generation, AdminAction{}); !errors.Is(err, ErrAccountConflict) {
			t.Fatalf("last enabled administrator removal allowed: %v", err)
		}
		current, err := s.GetAccount(ctx, admin.Username)
		if err != nil || !current.Enabled || !current.IsAdmin || current.Generation != admin.Generation {
			t.Fatalf("rejected change was not atomic: %+v %v", current, err)
		}
		if _, err = s.AuthenticateAccount(ctx, admin.Username, accountTestPassword); err != nil {
			t.Fatal("rejected change replaced password")
		}
	}
	second, err := s.CreateAccountWithRole(ctx, "second-admin", accountTestPassword, 10, true, AdminAction{})
	if err != nil {
		t.Fatal(err)
	}
	admin, err = s.UpdateAccountWithRole(ctx, admin.Username, "", nil, &notAdmin, admin.Generation, AdminAction{})
	if err != nil || admin.IsAdmin || admin.Generation != 2 {
		t.Fatalf("demote administrator: %+v %v", admin, err)
	}
	if _, err = s.UpdateAccountWithRole(ctx, second.Username, "", &disabled, nil, second.Generation, AdminAction{}); !errors.Is(err, ErrAccountConflict) {
		t.Fatalf("removed remaining administrator: %v", err)
	}
	isAdmin := true
	if _, err = s.UpdateAccountWithRole(ctx, admin.Username, "", nil, &isAdmin, 1, AdminAction{}); !errors.Is(err, ErrAccountConflict) {
		t.Fatalf("stale role change accepted: %v", err)
	}
	admin, err = s.UpdateAccountWithRole(ctx, admin.Username, "", nil, &isAdmin, admin.Generation, AdminAction{})
	if err != nil || !admin.IsAdmin || admin.Generation != 3 {
		t.Fatalf("promote administrator: %+v %v", admin, err)
	}
	second, err = s.UpdateAccount(ctx, second.Username, "", &disabled, second.Generation, AdminAction{})
	if err != nil || second.Enabled {
		t.Fatalf("disable second administrator: %+v %v", second, err)
	}
	if _, err = s.UpdateAccountWithRole(ctx, admin.Username, "", nil, &notAdmin, admin.Generation, AdminAction{}); !errors.Is(err, ErrAccountConflict) {
		t.Fatalf("disabled administrator incorrectly counted: %v", err)
	}
	if a, err := s.AuthenticateAccount(ctx, admin.Username, accountTestPassword); err != nil || !a.IsAdmin {
		t.Fatalf("authenticated role missing: %+v %v", a, err)
	}
	if _, err = s.GetAccount(ctx, "missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("unknown account read: %v", err)
	}
	s.commitUncertain.Store(true)
	if _, err = s.GetAccount(ctx, admin.Username); !errors.Is(err, ErrCommitOutcomeUnknown) {
		t.Fatalf("uncertain commit still authorizes account: %v", err)
	}
	s.commitUncertain.Store(false)
}

func TestProvisionAdminAccountPreservesDataAndInvalidatesCredentials(t *testing.T) {
	s := registrationStore(t)
	ctx := context.Background()
	a := createTestAccount(t, s, "existing", 3)
	fp := securityFingerprint("local-provision-device")
	if _, err := s.BindIdentity(ctx, "existing-device", fp, 0, AdminAction{}); err != nil {
		t.Fatal(err)
	}
	disabled := false
	a, err := s.UpdateAccount(ctx, a.Username, "", &disabled, a.Generation, AdminAction{})
	if err != nil {
		t.Fatal(err)
	}
	provisioned, err := s.ProvisionAdminAccount(ctx, a.Username, "local recovery password", AdminAction{Operator: "local-cli"})
	if err != nil || !provisioned.IsAdmin || !provisioned.Enabled || provisioned.Generation != a.Generation+1 || provisioned.MaxDevices != a.MaxDevices || provisioned.CreatedAt.UnixMicro() != a.CreatedAt.UnixMicro() {
		t.Fatalf("provision existing account: %+v %v", provisioned, err)
	}
	if _, err = s.AuthenticateAccount(ctx, a.Username, accountTestPassword); !errors.Is(err, ErrAccountAuth) {
		t.Fatalf("old password retained: %v", err)
	}
	if authenticated, err := s.AuthenticateAccount(ctx, a.Username, "local recovery password"); err != nil || !authenticated.IsAdmin {
		t.Fatalf("replacement administrator password unusable: %+v %v", authenticated, err)
	}
	if err = s.ValidateIdentity(ctx, fp, "existing-device"); err != nil {
		t.Fatalf("identity binding lost: %v", err)
	}
	created, err := s.ProvisionAdminAccount(ctx, "new-admin", accountTestPassword, AdminAction{Operator: "local-cli"})
	if err != nil || !created.IsAdmin || !created.Enabled || created.Generation != 1 {
		t.Fatalf("provision new account: %+v %v", created, err)
	}
	updated, err := s.ProvisionAdminAccount(ctx, created.Username, accountTestPassword, AdminAction{})
	if err != nil || updated.Generation != 2 {
		t.Fatalf("same password recovery did not invalidate credentials: %+v %v", updated, err)
	}
	actions, err := s.ListAdminActions(ctx, 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	var recoveryActions int
	for _, action := range actions {
		if action.Action == "provision_admin_account" {
			recoveryActions++
		}
	}
	if recoveryActions != 3 {
		t.Fatalf("expected atomic recovery audit, got %d actions", recoveryActions)
	}
}
