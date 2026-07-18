package teaching

import (
	"net/http"

	"github.com/google/uuid"
)

type adminAudienceRequest struct {
	Mode    AudienceMode `json:"mode"`
	UserIDs []string     `json:"userIds"`
}
type adminVideoRequest struct {
	ID          string `json:"id"`
	URL         string `json:"url"`
	Title       string `json:"title"`
	Description string `json:"description"`
	SortKey     int64  `json:"sortKey"`
}

func adminDraftChildren(w http.ResponseWriter, r *http.Request, a adminAudienceRequest, videos []adminVideoRequest) (Audience, []ExternalVideo, bool) {
	audience := Audience{Mode: a.Mode, UserIDs: make([]uuid.UUID, 0, len(a.UserIDs))}
	for _, raw := range a.UserIDs {
		id, err := uuid.Parse(raw)
		if err != nil || id == uuid.Nil {
			adminBad(w, r)
			return Audience{}, nil, false
		}
		audience.UserIDs = append(audience.UserIDs, id)
	}
	out := make([]ExternalVideo, 0, len(videos))
	for _, input := range videos {
		id, err := uuid.Parse(input.ID)
		if err != nil || id == uuid.Nil {
			adminBad(w, r)
			return Audience{}, nil, false
		}
		out = append(out, ExternalVideo{ID: id, URL: input.URL, Title: input.Title, Description: input.Description, SortKey: input.SortKey})
	}
	return audience, out, true
}
