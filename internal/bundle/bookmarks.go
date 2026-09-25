package bundle

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"
)

const maxBookmarkLength = 40
const maxBookmarkHashPrefix = maxBookmarkLength - len("m_")

type BookmarkCandidate struct {
	Logical string
	Label   string
}

type BookmarkHash func(string) []byte

func GenerateBookmarks(candidates []BookmarkCandidate, hashFunction BookmarkHash) (map[string]string, error) {
	if hashFunction == nil {
		hashFunction = func(value string) []byte {
			sum := sha256.Sum256([]byte(value))
			return sum[:]
		}
	}
	byLogical := map[string]BookmarkCandidate{}
	for _, candidate := range candidates {
		if candidate.Logical == "" {
			return nil, errors.New("bookmark logical identity is required")
		}
		if _, exists := byLogical[candidate.Logical]; exists {
			return nil, fmt.Errorf("duplicate bookmark logical identity %q", candidate.Logical)
		}
		byLogical[candidate.Logical] = candidate
	}
	logicals := make([]string, 0, len(byLogical))
	digests := map[string]string{}
	prefixes := map[string]int{}
	for logical := range byLogical {
		logicals = append(logicals, logical)
		digest := hex.EncodeToString(hashFunction(logical))
		if digest == "" {
			return nil, fmt.Errorf("bookmark hash for %q is empty", logical)
		}
		digests[logical] = digest
		prefixes[logical] = min(12, min(len(digest), maxBookmarkHashPrefix))
	}
	sort.Strings(logicals)
	for attempts := 0; attempts <= 64; attempts++ {
		result := map[string]string{}
		groups := map[string][]string{}
		for _, logical := range logicals {
			name := bookmarkName(digests[logical], prefixes[logical], byLogical[logical].Label)
			result[logical] = name
			groups[name] = append(groups[name], logical)
		}
		collided := false
		for name, group := range groups {
			if len(group) < 2 {
				continue
			}
			collided = true
			for _, logical := range group {
				maximum := min(len(digests[logical]), maxBookmarkHashPrefix)
				if prefixes[logical] >= maximum {
					return nil, fmt.Errorf("bookmark collision %q cannot be resolved for %s", name, strings.Join(group, ", "))
				}
				prefixes[logical] = min(prefixes[logical]+2, maximum)
			}
		}
		if !collided {
			return result, nil
		}
	}
	return nil, errors.New("bookmark collisions could not be resolved")
}

func bookmarkName(digest string, prefixLength int, label string) string {
	prefix := digest[:prefixLength]
	base := "m_" + prefix
	slug := asciiSlug(label)
	if slug == "" || len(base)+1 >= maxBookmarkLength {
		return base
	}
	available := maxBookmarkLength - len(base) - 1
	if len(slug) > available {
		slug = slug[:available]
	}
	return base + "_" + slug
}

func asciiSlug(value string) string {
	var builder strings.Builder
	underscore := false
	for _, character := range strings.ToLower(value) {
		if character <= unicode.MaxASCII && ((character >= 'a' && character <= 'z') || (character >= '0' && character <= '9')) {
			builder.WriteRune(character)
			underscore = false
			continue
		}
		if builder.Len() > 0 && !underscore {
			builder.WriteByte('_')
			underscore = true
		}
	}
	return strings.Trim(builder.String(), "_")
}
