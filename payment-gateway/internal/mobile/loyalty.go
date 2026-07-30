package mobile

import "net/http"

func mobileLoyaltyPointsForUser(_ string) int {
	return 0
}

func (s *Server) handleLoyaltyPoints(w http.ResponseWriter, r *http.Request) {
	uid := userIDFromCtx(r)
	points := mobileLoyaltyPointsForUser(uid)
	writeJSON(w, http.StatusOK, map[string]any{
		"points":         points,
		"loyalty_points": points,
	})
}
