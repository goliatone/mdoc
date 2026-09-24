package fields

import "time"

type PlanComputed struct {
	ProjectID        string
	Profile          string
	PublicationID    string
	PublicationKind  string
	PublicationTitle string
}

type FrozenComputed struct {
	OperationID string    `json:"operation_id"`
	ReviewSetID string    `json:"review_set_id"`
	Generation  int       `json:"generation"`
	PublishedAt time.Time `json:"published_at"`
}

func PlanComputedValues(input PlanComputed) map[string]any {
	publication := map[string]any{
		"id": input.PublicationID, "kind": input.PublicationKind, "title": input.PublicationTitle,
	}
	result := map[string]any{
		"project":     map[string]any{"id": input.ProjectID},
		"profile":     input.Profile,
		"publication": publication,
	}
	if input.PublicationKind == "bundle" {
		result["bundle"] = map[string]any{"id": input.PublicationID, "title": input.PublicationTitle}
	}
	return result
}

func FreezeComputed(plan map[string]any, frozen FrozenComputed) map[string]any {
	result := cloneComputedMap(plan)
	result["operation_id"] = frozen.OperationID
	result["review_set_id"] = frozen.ReviewSetID
	result["generation"] = frozen.Generation
	result["published_at"] = frozen.PublishedAt.UTC().Format(time.RFC3339Nano)
	return result
}

func cloneComputedMap(input map[string]any) map[string]any {
	result := make(map[string]any, len(input))
	for key, value := range input {
		if nested, ok := value.(map[string]any); ok {
			result[key] = cloneComputedMap(nested)
		} else {
			result[key] = value
		}
	}
	return result
}
