package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
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
	fp := securityFingerprint("administrator-device")
	if err = s.EnrollAccountDevice(ctx, admin.Username, admin.Generation, "admin-device", fp, "", AdminAction{}); err != nil {
		t.Fatal(err)
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
	assertAccountKey(t, s, fp, true, false)
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
	if err := s.EnrollAccountDevice(ctx, a.Username, a.Generation, "existing-device", fp, "kept device", AdminAction{}); err != nil {
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
	device, err := s.AccountDeviceForKey(ctx, fp)
	if err != nil || device.ClientID != "existing-device" || device.Name != "kept device" || device.Active {
		t.Fatalf("existing device mutated or not invalidated: %+v %v", device, err)
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

func TestAdminAccountMigrationPreservesV4AccountsDevicesAndBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v4.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	a := createTestAccount(t, s, "existing", 4)
	fp := securityFingerprint("v4-account-device")
	if err = s.EnrollAccountDevice(ctx, a.Username, a.Generation, "original-device", fp, "original name", AdminAction{}); err != nil {
		t.Fatal(err)
	}
	var originalHash string
	if err = s.db.QueryRow(`SELECT password_hash FROM accounts WHERE username=?`, a.Username).Scan(&originalHash); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{`DROP TABLE client_update_policy`, `ALTER TABLE accounts DROP COLUMN is_admin`, `PRAGMA user_version=4`} {
		if _, err = s.db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	current, err := s.GetAccount(ctx, a.Username)
	if err != nil || current.IsAdmin || !current.Enabled || current.Generation != a.Generation || current.MaxDevices != a.MaxDevices || current.CreatedAt.UnixMicro() != a.CreatedAt.UnixMicro() {
		t.Fatalf("migrated account changed: %+v %v", current, err)
	}
	var migratedHash string
	if err = s.db.QueryRow(`SELECT password_hash FROM accounts WHERE username=?`, a.Username).Scan(&migratedHash); err != nil || migratedHash != originalHash {
		t.Fatal("migration replaced password hash")
	}
	assertAccountKey(t, s, fp, true, true)
	device, err := s.AccountDeviceForKey(ctx, fp)
	if err != nil || device.ClientID != "original-device" || device.Name != "original name" {
		t.Fatalf("device data changed: %+v %v", device, err)
	}
	if _, err = s.AuthenticateAccount(ctx, a.Username, accountTestPassword); err != nil {
		t.Fatal(err)
	}
	backup, err := sql.Open("sqlite", s.BackupPath())
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	var backupVersion, currentVersion int
	if err = backup.QueryRow(`PRAGMA user_version`).Scan(&backupVersion); err != nil || backupVersion != 4 {
		t.Fatalf("backup version=%d %v", backupVersion, err)
	}
	if err = s.db.QueryRow(`PRAGMA user_version`).Scan(&currentVersion); err != nil || currentVersion != schemaVersion {
		t.Fatalf("current version=%d %v", currentVersion, err)
	}
	var backupHash string
	if err = backup.QueryRow(`SELECT password_hash FROM accounts WHERE username=?`, a.Username).Scan(&backupHash); err != nil || backupHash != originalHash {
		t.Fatal("backup lost old account")
	}
}
