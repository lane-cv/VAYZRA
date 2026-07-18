package teaching

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"happylearn.local/app/internal/platform/httpx"
)

type adminCatalogDTO struct {
	ID          string `json:"id"`
	ParentID    string `json:"parentId,omitempty"`
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	Description string `json:"description"`
	SortKey     int64  `json:"sortKey"`
	Status      string `json:"status"`
	// Published is retained for compatibility and means a current revision pointer exists.
	Published bool `json:"published"`
}
type adminAudienceDTO struct {
	Mode    AudienceMode `json:"mode"`
	UserIDs []string     `json:"userIds"`
}
type adminVideoDTO struct {
	ID          string `json:"id"`
	URL         string `json:"url"`
	Title       string `json:"title"`
	Description string `json:"description"`
	SortKey     int64  `json:"sortKey"`
}
type adminDraftDTO struct {
	LessonID       string           `json:"lessonId"`
	ChapterID      string           `json:"chapterId"`
	Title          string           `json:"title"`
	Summary        string           `json:"summary"`
	BodyMarkdown   string           `json:"bodyMarkdown"`
	SortKey        int64            `json:"sortKey"`
	LockVersion    int64            `json:"lockVersion"`
	Audience       adminAudienceDTO `json:"audience"`
	ExternalVideos []adminVideoDTO  `json:"externalVideos"`
	UpdatedAt      string           `json:"updatedAt"`
}
type adminRevisionDTO struct {
	ID                 string           `json:"id"`
	LessonID           string           `json:"lessonId"`
	Version            int64            `json:"version"`
	SourceDraftVersion int64            `json:"sourceDraftVersion"`
	Title              string           `json:"title"`
	Summary            string           `json:"summary"`
	BodyMarkdown       string           `json:"bodyMarkdown"`
	SortKey            int64            `json:"sortKey"`
	Audience           adminAudienceDTO `json:"audience"`
	ExternalVideos     []adminVideoDTO  `json:"externalVideos"`
	PublishedBy        string           `json:"publishedBy"`
	PublishedAt        string           `json:"publishedAt"`
}
type adminLessonDTO struct {
	ID                  string            `json:"id"`
	ChapterID           string            `json:"chapterId"`
	Status              string            `json:"status"`
	PublishedRevisionID string            `json:"publishedRevisionId,omitempty"`
	Draft               adminDraftDTO     `json:"draft"`
	CurrentPublication  *adminRevisionDTO `json:"currentPublication"`
}

func adminLessonStatus(archived, published, hasRevisions bool) string {
	switch {
	case archived:
		return "archived"
	case published:
		return "published"
	case hasRevisions:
		return "withdrawn"
	default:
		return "draft"
	}
}

func adminAudienceView(a Audience) adminAudienceDTO {
	out := adminAudienceDTO{Mode: a.Mode, UserIDs: make([]string, 0, len(a.UserIDs))}
	for _, id := range a.UserIDs {
		out.UserIDs = append(out.UserIDs, id.String())
	}
	return out
}
func adminVideoViews(videos []ExternalVideo) []adminVideoDTO {
	out := make([]adminVideoDTO, 0, len(videos))
	for _, v := range videos {
		out = append(out, adminVideoDTO{ID: v.ID.String(), URL: v.URL, Title: v.Title, Description: v.Description, SortKey: v.SortKey})
	}
	return out
}
func adminDraftView(d Draft) adminDraftDTO {
	return adminDraftDTO{LessonID: d.LessonID.String(), ChapterID: d.ChapterID.String(), Title: d.Title, Summary: d.Summary, BodyMarkdown: d.BodyMarkdown, SortKey: d.SortKey, LockVersion: d.LockVersion, Audience: adminAudienceView(d.Audience), ExternalVideos: adminVideoViews(d.ExternalVideos), UpdatedAt: d.UpdatedAt.UTC().Format(time.RFC3339Nano)}
}
func adminRevisionView(r Revision) adminRevisionDTO {
	return adminRevisionDTO{ID: r.ID.String(), LessonID: r.LessonID.String(), Version: r.Version, SourceDraftVersion: r.SourceDraftVersion, Title: r.Title, Summary: r.Summary, BodyMarkdown: r.BodyMarkdown, SortKey: r.SortKey, Audience: adminAudienceView(r.Audience), ExternalVideos: adminVideoViews(r.ExternalVideos), PublishedBy: r.PublishedBy.String(), PublishedAt: r.PublishedAt.UTC().Format(time.RFC3339Nano)}
}
func adminCatalogNodeView(n CatalogNode) adminCatalogDTO {
	status := "active"
	if n.ArchivedAt != nil {
		status = "archived"
	}
	p := ""
	if n.ParentID != uuid.Nil {
		p = n.ParentID.String()
	}
	return adminCatalogDTO{ID: n.ID.String(), ParentID: p, Kind: string(n.Kind), Name: n.Name, Description: n.Description, SortKey: n.SortKey, Status: status}
}

func (h *AdminHandler) ListCatalog(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	for key := range q {
		if key != "kind" && key != "parentId" && key != "includeArchived" && key != "limit" && key != "cursor" {
			adminBad(w, r)
			return
		}
	}
	limit := 100
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			adminBad(w, r)
			return
		}
		limit = n
	}
	if limit < 1 || limit > 200 {
		adminBad(w, r)
		return
	}
	parent, ok := optionalUUID(w, r, q.Get("parentId"))
	if !ok {
		return
	}
	after, ok := decodeAdminCatalogCursor(w, r, q.Get("cursor"))
	if !ok {
		return
	}
	include := false
	if raw := q.Get("includeArchived"); raw != "" {
		var err error
		include, err = strconv.ParseBool(raw)
		if err != nil {
			adminBad(w, r)
			return
		}
	}
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	items, next, err := h.service.ListAdminCatalog(r.Context(), actor, AdminCatalogInput{Kind: q.Get("kind"), ParentID: parent, IncludeArchived: include, Limit: limit, After: after})
	if err != nil {
		adminError(w, r, err)
		return
	}
	views := make([]adminCatalogDTO, 0, len(items))
	for _, item := range items {
		status := "active"
		if item.Kind == "lesson" {
			status = adminLessonStatus(item.ArchivedAt != nil, item.Published, item.HasRevisions)
		} else if item.ArchivedAt != nil {
			status = "archived"
		}
		parentID := ""
		if item.ParentID != uuid.Nil {
			parentID = item.ParentID.String()
		}
		views = append(views, adminCatalogDTO{ID: item.ID.String(), ParentID: parentID, Kind: item.Kind, Name: item.Name, Description: item.Description, SortKey: item.SortKey, Status: status, Published: item.Published})
	}
	httpx.JSON(w, http.StatusOK, struct {
		Data       []adminCatalogDTO `json:"data"`
		NextCursor string            `json:"nextCursor,omitempty"`
	}{views, encodeAdminCatalogCursor(next)})
}
func (h *AdminHandler) GetLesson(w http.ResponseWriter, r *http.Request) {
	id, ok := routeUUID(w, r, "id")
	if !ok {
		return
	}
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	detail, err := h.service.GetAdminLesson(r.Context(), actor, id)
	if err != nil {
		adminError(w, r, err)
		return
	}
	status := adminLessonStatus(detail.Lesson.ArchivedAt != nil, detail.Lesson.PublishedRevisionID != uuid.Nil, detail.HasRevisions)
	view := adminLessonDTO{ID: detail.Lesson.ID.String(), ChapterID: detail.Lesson.ChapterID.String(), Status: status, Draft: adminDraftView(detail.Draft)}
	if detail.Lesson.PublishedRevisionID != uuid.Nil {
		view.PublishedRevisionID = detail.Lesson.PublishedRevisionID.String()
	}
	if detail.Published != nil {
		p := adminRevisionView(*detail.Published)
		view.CurrentPublication = &p
	}
	httpx.JSON(w, http.StatusOK, struct {
		Data adminLessonDTO `json:"data"`
	}{view})
}
func (h *AdminHandler) ListRevisions(w http.ResponseWriter, r *http.Request) {
	id, ok := routeUUID(w, r, "id")
	if !ok {
		return
	}
	q := r.URL.Query()
	for key := range q {
		if key != "limit" && key != "cursor" {
			adminBad(w, r)
			return
		}
	}
	limit := 20
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			adminBad(w, r)
			return
		}
		limit = n
	}
	if limit < 1 || limit > 100 {
		adminBad(w, r)
		return
	}
	after, ok := decodeRevisionCursor(w, r, q.Get("cursor"))
	if !ok {
		return
	}
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	revs, next, err := h.service.ListAdminRevisions(r.Context(), actor, id, limit, after)
	if err != nil {
		adminError(w, r, err)
		return
	}
	views := make([]adminRevisionDTO, 0, len(revs))
	for _, rev := range revs {
		views = append(views, adminRevisionView(rev))
	}
	httpx.JSON(w, http.StatusOK, struct {
		Data       []adminRevisionDTO `json:"data"`
		NextCursor string             `json:"nextCursor,omitempty"`
	}{views, encodeRevisionCursor(next)})
}

func encodeAdminCatalogCursor(c AdminCatalogCursor) string {
	if c.ID == uuid.Nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("%d:%d:%s", c.Rank, c.SortKey, c.ID)))
}
func decodeAdminCatalogCursor(w http.ResponseWriter, r *http.Request, raw string) (AdminCatalogCursor, bool) {
	if raw == "" {
		return AdminCatalogCursor{}, true
	}
	if len(raw) > 128 {
		adminBad(w, r)
		return AdminCatalogCursor{}, false
	}
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		adminBad(w, r)
		return AdminCatalogCursor{}, false
	}
	parts := strings.Split(string(b), ":")
	if len(parts) != 3 {
		adminBad(w, r)
		return AdminCatalogCursor{}, false
	}
	rank, err1 := strconv.Atoi(parts[0])
	sort, err2 := strconv.ParseInt(parts[1], 10, 64)
	id, err3 := uuid.Parse(parts[2])
	if err1 != nil || err2 != nil || err3 != nil || rank < 1 || rank > 5 || id == uuid.Nil {
		adminBad(w, r)
		return AdminCatalogCursor{}, false
	}
	return AdminCatalogCursor{Rank: rank, SortKey: sort, ID: id}, true
}
func encodeRevisionCursor(c RevisionCursor) string {
	if c.ID == uuid.Nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("%d:%s", c.Version, c.ID)))
}
func decodeRevisionCursor(w http.ResponseWriter, r *http.Request, raw string) (RevisionCursor, bool) {
	if raw == "" {
		return RevisionCursor{}, true
	}
	if len(raw) > 96 {
		adminBad(w, r)
		return RevisionCursor{}, false
	}
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		adminBad(w, r)
		return RevisionCursor{}, false
	}
	vRaw, idRaw, ok := strings.Cut(string(b), ":")
	if !ok {
		adminBad(w, r)
		return RevisionCursor{}, false
	}
	v, e1 := strconv.ParseInt(vRaw, 10, 64)
	id, e2 := uuid.Parse(idRaw)
	if e1 != nil || e2 != nil || v < 1 || id == uuid.Nil {
		adminBad(w, r)
		return RevisionCursor{}, false
	}
	return RevisionCursor{Version: v, ID: id}, true
}
