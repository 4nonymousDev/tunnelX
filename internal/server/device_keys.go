package server

func (s *Server) fingerprintAuthorized(fp string) bool {
	if s.store != nil {
		ctx, cancel := s.dbContext()
		known, allowed, err := s.store.DeviceKeyStatus(ctx, fp)
		cancel()
		if err != nil {
			return false
		}
		if known {
			return allowed
		}
	}
	return s.auth.AuthorizedFingerprint(fp)
}
