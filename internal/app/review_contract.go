package app

import (
	"fmt"
	"strings"

	"github.com/goliatone/mdoc/internal/config"
)

func SelectReviewPublication(profile config.SelectedProfile, requested string) (string, config.PublicationConfig, error) {
	selected := strings.TrimSpace(requested)
	if selected == "" {
		selected = strings.TrimSpace(profile.EntryPublication)
	}
	if selected == "" {
		return "", config.PublicationConfig{}, NewError(ClassValidation, string(ReviewCodePublicationInvalid), "review pull requires a version 3 entry publication")
	}
	publication, ok := profile.Publications[selected]
	if !ok {
		return "", config.PublicationConfig{}, NewError(ClassValidation, string(ReviewCodePublicationInvalid), fmt.Sprintf("publication %q is not defined", selected))
	}
	if publication.Kind != config.PublicationSource && publication.Kind != config.PublicationBundle {
		return "", config.PublicationConfig{}, NewError(ClassValidation, string(ReviewCodePublicationInvalid), fmt.Sprintf("publication %q has unsupported kind %q", selected, publication.Kind))
	}
	if !publication.Review.Pull.Enabled {
		return "", config.PublicationConfig{}, NewError(ClassValidation, string(ReviewCodePullDisabled), fmt.Sprintf("review pull is disabled for publication %q; enable publications.%s.review.pull.enabled and publish a new generation", selected, selected))
	}
	return selected, publication, nil
}
