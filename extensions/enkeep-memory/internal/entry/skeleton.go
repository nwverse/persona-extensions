package entry

import "strings"

// SkeletonPredicates are the structural relation predicates retained as the entity recall skeleton
// (design §2.2, l0-leverage §13): ownership, assignment, dependencies, team membership, collaboration, role,
// and reporting structure.
var SkeletonPredicates = map[string]bool{
	"assigned":          true,
	"collaborates_with": true,
	"depends_on":        true,
	"member_of":         true,
	"owns":              true,
	"reports_to":        true,
	"role":              true,
}

// IsSkeletonPredicate says whether p is one of the structural skeleton relation predicates.
func IsSkeletonPredicate(p string) bool {
	return SkeletonPredicates[strings.ToLower(strings.TrimSpace(p))]
}
