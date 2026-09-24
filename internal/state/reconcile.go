package state

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/goliatone/mdoc/internal/sourcekey"
)

type RemoteFolder struct {
	FileID string
	Role   string
}

type RemoteTarget struct {
	Target
	SourceKey       string
	TargetKey       string
	PublicationID   string
	PublicationKind string
	Members         []string
	ExpectedSetSize int
	ParentID        string
}

type ReconcileInput struct {
	WorkspaceID string
	Profile     string
	Folders     []RemoteFolder
	Targets     []RemoteTarget
}

type ReconcileResult struct {
	State       *State
	PendingSets []string
}

func Reconcile(input ReconcileInput) (ReconcileResult, error) {
	result := ReconcileResult{State: New(input.WorkspaceID, input.Profile)}
	for _, role := range []string{"staging", "review"} {
		matches := []string{}
		for _, folder := range input.Folders {
			if folder.Role == role {
				matches = append(matches, folder.FileID)
			}
		}
		if len(matches) > 1 {
			return result, fmt.Errorf("duplicate %s folder matches: %v", role, matches)
		}
		if len(matches) == 1 {
			if role == "staging" {
				result.State.Folders.StagingID = matches[0]
			} else {
				result.State.Folders.ReviewID = matches[0]
			}
		}
	}

	sets := map[string][]RemoteTarget{}
	seen := map[string]string{}
	for _, target := range input.Targets {
		identity := target.TargetKey
		if identity == "" {
			normalized, err := sourcekey.Normalize(target.SourceKey)
			if err != nil {
				return result, fmt.Errorf("remote target %s has invalid source key %q: %w", target.FileID, target.SourceKey, err)
			}
			target.SourceKey = normalized
			identity = "source:" + normalized
			target.PublicationKind = "source"
		} else {
			if err := validateTargetKey(identity); err != nil || target.PublicationID == "" || identity != "publication:"+target.PublicationID || target.PublicationKind != "source" && target.PublicationKind != "bundle" {
				return result, fmt.Errorf("remote target %s has invalid publication identity %q", target.FileID, identity)
			}
			if target.PublicationKind == "source" {
				normalized, err := sourcekey.Normalize(target.SourceKey)
				if err != nil || normalized != target.SourceKey {
					return result, fmt.Errorf("remote target %s has invalid source input %q", target.FileID, target.SourceKey)
				}
			} else if target.SourceKey != "" {
				return result, fmt.Errorf("remote bundle target %s has unexpected source input", target.FileID)
			}
		}
		target.TargetKey = identity
		key := identity + "\x00" + strconv.Itoa(target.Generation)
		if prior, exists := seen[key]; exists {
			return result, fmt.Errorf("duplicate target for %q generation %d: %s and %s", identity, target.Generation, prior, target.FileID)
		}
		seen[key] = target.FileID
		sets[target.ReviewSetID] = append(sets[target.ReviewSetID], target)
	}

	complete := []RemoteTarget{}
	for setID, targets := range sets {
		if !completeSet(targets, result.State.Folders.ReviewID) {
			result.PendingSets = append(result.PendingSets, setID)
			continue
		}
		complete = append(complete, targets...)
	}
	sort.Strings(result.PendingSets)
	sort.Slice(complete, func(i, j int) bool {
		if complete[i].TargetKey != complete[j].TargetKey {
			return complete[i].TargetKey < complete[j].TargetKey
		}
		return complete[i].Generation < complete[j].Generation
	})
	for _, remote := range complete {
		document := result.State.Targets[remote.TargetKey]
		target := remote.Target
		if document.ActiveTarget == nil || target.Generation > document.ActiveTarget.Generation {
			if document.ActiveTarget != nil {
				document.PriorTargets = append(document.PriorTargets, *document.ActiveTarget)
			}
			document.ActiveTarget = &target
		} else {
			document.PriorTargets = append(document.PriorTargets, target)
		}
		document.TargetKey = remote.TargetKey
		document.PublicationID = remote.PublicationID
		document.PublicationKind = remote.PublicationKind
		document.Source = remote.SourceKey
		document.Members = append([]string(nil), remote.Members...)
		result.State.Targets[remote.TargetKey] = document
		if strings.HasPrefix(remote.TargetKey, "source:") {
			result.State.Documents[remote.SourceKey] = document
		}
	}
	return result, result.State.Validate()
}

func completeSet(targets []RemoteTarget, reviewFolderID string) bool {
	return ValidateCompleteSet(targets, reviewFolderID) == nil
}

// ValidateCompleteSet verifies that remote metadata describes one atomic ready set.
func ValidateCompleteSet(targets []RemoteTarget, reviewFolderID string) error {
	if len(targets) == 0 || reviewFolderID == "" {
		return errors.New("review set has no members or review folder")
	}
	expected := targets[0].ExpectedSetSize
	operationID := targets[0].OperationID
	reviewSetID := targets[0].ReviewSetID
	if expected < 1 || len(targets) != expected || operationID == "" || reviewSetID == "" {
		return fmt.Errorf("review set has %d members, expected %d, operation %q", len(targets), expected, operationID)
	}
	targetKeys := map[string]bool{}
	for _, target := range targets {
		if target.ExpectedSetSize != expected || target.OperationID != operationID || target.ReviewSetID != reviewSetID || target.PublishStatus != "ready" || target.ParentID != reviewFolderID || target.TargetKey == "" {
			return fmt.Errorf("review set member %s has inconsistent metadata", target.FileID)
		}
		if targetKeys[target.TargetKey] {
			return fmt.Errorf("review set repeats target %q", target.TargetKey)
		}
		targetKeys[target.TargetKey] = true
	}
	if len(targetKeys) != expected {
		return fmt.Errorf("review set has %d unique targets, expected %d", len(targetKeys), expected)
	}
	return nil
}

var ErrUnsafeRecovery = errors.New("remote state cannot be recovered safely")
