package server

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"tunnelx/internal/adminapi"
	"tunnelx/internal/keygen"
	"tunnelx/internal/store"
)

// A public-key upload is an explicit recovery/rotation of an existing identity.
// A fingerprint-only request retains the existing preauthorized-key workflow.
func parseReplacementKey(r adminapi.BindIdentityRequest) (string, keygen.Metadata, error) {
	var metadata keygen.Metadata
	invalid := func(message string) (string, keygen.Metadata, error) {
		return "", metadata, &adminapi.BackendError{Code: "invalid_public_key", Message: message}
	}
	if !store.ValidClientID(r.ClientID) || r.ExpectedGeneration < 1 || r.ExpectedGeneration == int64(^uint64(0)>>1) || len(r.PublicKey) > 16384 {
		return invalid("replacement requires a valid device ID, current generation and one public key")
	}
	key, comment, options, rest, err := ssh.ParseAuthorizedKey([]byte(strings.TrimSpace(r.PublicKey)))
	if err != nil || len(bytes.TrimSpace(rest)) != 0 || len(options) != 0 {
		return invalid("provide exactly one public key without authorized_keys options")
	}
	if strings.HasPrefix(strings.TrimSpace(comment), "tunnelx:") {
		metadata, err = keygen.ParseMetadataComment(comment)
		if err != nil {
			return invalid(err.Error())
		}
	}
	fp := ssh.FingerprintSHA256(key)
	if r.Fingerprint != "" && r.Fingerprint != fp {
		return invalid("public key does not match the supplied fingerprint")
	}
	return fp, metadata, nil
}

// Called with the policy writer held. File authorization has its own durable
// operation because it cannot share the subsequent SQLite binding transaction.
// If binding fails, the old identity remains in effect and the admin can retry;
// reconciliation of this operation only verifies the public-key import.
func (s *Server) importReplacementKey(ctx context.Context, r adminapi.BindIdentityRequest, metadata keygen.Metadata) error {
	op, err := s.store.BeginAdminOperation(ctx, s.action(ctx, r.Fingerprint, "import_public_key", r.Reason, "pending", ""))
	if err != nil {
		return err
	}
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	comment, err := metadata.Comment()
	if err == nil {
		_, err = s.auth.Import(r.PublicKey, comment)
	}
	if err == nil || errors.Is(err, errAuthorizedKeyExists) {
		// Recovery/retries must preserve existing client metadata and history.
		_, err = s.store.GetClient(finishCtx, r.Fingerprint)
		if errors.Is(err, sql.ErrNoRows) {
			err = s.store.ImportClient(finishCtx, r.Fingerprint, metadata.Username, metadata.Email, metadata.ComputerName, time.Now())
		}
	}
	state, errText := "applied", ""
	if err != nil {
		state, errText = "needs_reconcile", err.Error()
	}
	if errors.Is(err, store.ErrCommitOutcomeUnknown) {
		s.store.MarkAdminOperationUncertain(op)
		return err
	}
	if finishErr := s.store.CompleteAdminOperation(finishCtx, op, state, errText); finishErr != nil {
		s.store.MarkAdminOperationUncertain(op)
		if errors.Is(finishErr, store.ErrCommitOutcomeUnknown) {
			return finishErr
		}
		return &adminapi.PendingOperationError{ID: op}
	}
	if err != nil {
		return &adminapi.PendingOperationError{ID: op}
	}
	return nil
}
