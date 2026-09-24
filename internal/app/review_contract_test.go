package app

import (
	"errors"
	"testing"

	"github.com/goliatone/mdoc/internal/config"
)

func TestSelectReviewPublicationDefaultsToEntryAndRejectsUnsafeTargets(t *testing.T) {
	enabled := config.PublicationConfig{Kind: config.PublicationBundle, Review: config.ReviewConfig{Pull: config.ReviewPullConfig{Enabled: true}}}
	disabled := config.PublicationConfig{Kind: config.PublicationSource}
	profile := config.SelectedProfile{Name: "review", Profile: config.Profile{
		EntryPublication: "report",
		Publications: map[string]config.PublicationConfig{
			"report": enabled,
			"draft":  disabled,
		},
	}}
	selected, got, err := SelectReviewPublication(profile, "")
	if err != nil || selected != "report" || !got.Review.Pull.Enabled {
		t.Fatalf("default selection = %q, %#v, %v", selected, got, err)
	}
	if _, _, err := SelectReviewPublication(profile, "missing"); errorCode(err) != string(ReviewCodePublicationInvalid) {
		t.Fatalf("unknown publication error = %v", err)
	}
	if _, _, err := SelectReviewPublication(profile, "draft"); errorCode(err) != string(ReviewCodePullDisabled) {
		t.Fatalf("disabled publication error = %v", err)
	}
}

func errorCode(err error) string {
	var typed *Error
	if errors.As(err, &typed) {
		return typed.Code
	}
	return ""
}
