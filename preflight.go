//  Copyright (c) 2024 Couchbase, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// 		http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package bleve

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/blevesearch/bleve/v2/mapping"
	"github.com/blevesearch/bleve/v2/search"
	"github.com/blevesearch/bleve/v2/search/query"
)

// FieldCapability describes a capability that an indexed field must provide
// for a query clause (or a sort/kNN request) to execute against an index.
type FieldCapability uint32

const (
	// CapabilityIndexed means the field is indexed (inverted index terms
	// are available for term, phrase, range, boolean and geo style clauses).
	CapabilityIndexed FieldCapability = 1 << iota
	// CapabilityTermVectors means the field records term vectors, which are
	// required to evaluate phrase clauses.
	CapabilityTermVectors
	// CapabilityDocValues means the field maintains doc values, which are
	// required for field based sorting.
	CapabilityDocValues
	// CapabilityVector means the field is indexed as a vector field, which
	// is required for kNN clauses.
	CapabilityVector
)

// String returns a pipe separated list of the capability names.
func (c FieldCapability) String() string {
	var names []string
	if c&CapabilityIndexed != 0 {
		names = append(names, "indexed")
	}
	if c&CapabilityTermVectors != 0 {
		names = append(names, "term_vectors")
	}
	if c&CapabilityDocValues != 0 {
		names = append(names, "doc_values")
	}
	if c&CapabilityVector != 0 {
		names = append(names, "vector")
	}
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, "|")
}

// Machine readable reason codes reported by a preflight evaluation.
const (
	// PreflightReasonDefaultFieldMissing means a clause did not name a field
	// and the index mapping does not configure a default search field.
	PreflightReasonDefaultFieldMissing = "default_field_missing"
	// PreflightReasonFieldNotMapped means the referenced field has no
	// mapping in the index.
	PreflightReasonFieldNotMapped = "field_not_mapped"
	// PreflightReasonNotIndexed means the field exists but is not indexed.
	PreflightReasonNotIndexed = "not_indexed"
	// PreflightReasonMissingTermVectors means the field is indexed but does
	// not record term vectors while a phrase clause requires them.
	PreflightReasonMissingTermVectors = "missing_term_vectors"
	// PreflightReasonMissingDocValues means the field is indexed but does not
	// maintain doc values while a sort clause requires them.
	PreflightReasonMissingDocValues = "missing_doc_values"
	// PreflightReasonNotVectorField means a kNN clause targets a field that
	// is not mapped as a vector field.
	PreflightReasonNotVectorField = "not_vector_field"
	// PreflightReasonVectorDimMismatch means a kNN clause query vector has a
	// different dimensionality than the indexed vector field.
	PreflightReasonVectorDimMismatch = "vector_dim_mismatch"
)

// A ClauseRequirement describes the field capabilities a single clause of a
// SearchRequest needs. Requirements are derived from the request alone and
// are independent of any concrete index.
type ClauseRequirement struct {
	// Path locates the clause within the request, for example "must[0]",
	// "should[1].filter", "conjuncts[2]", "sort[0]" or "knn[0]".
	Path string `json:"path"`
	// Clause is the clause kind, for example "term", "match", "phrase",
	// "match_phrase", "numeric_range", "sort", "sort_geo_distance" or "knn".
	Clause string `json:"clause"`
	// Field is the field named by the clause. An empty string means the
	// clause relies on the index mapping's default search field, which is
	// resolved per index during evaluation.
	Field string `json:"field,omitempty"`
	// Needs is the set of capabilities the field must provide.
	Needs FieldCapability `json:"needs"`
	// VectorDims is the dimensionality of the query vector for kNN clauses.
	VectorDims int `json:"vector_dims,omitempty"`
}

// A CapabilityProblem describes why a single clause cannot be served by a
// specific index.
type CapabilityProblem struct {
	Path   string `json:"path"`
	Clause string `json:"clause"`
	// Field is the resolved field name evaluated against the index.
	Field string          `json:"field"`
	Needs FieldCapability `json:"needs"`
	// Code is one of the PreflightReason* constants.
	Code   string `json:"code"`
	Reason string `json:"reason"`
}

// IndexPreflightReport is the preflight evaluation of a single (leaf) index.
type IndexPreflightReport struct {
	Name     string               `json:"name"`
	OK       bool                 `json:"ok"`
	Problems []*CapabilityProblem `json:"problems,omitempty"`
}

// A PreflightReport is the result of an optional query capability preflight
// performed before a search is executed.
//
// Requirements lists every clause of the request together with the field
// capabilities it needs. Indexes holds the per index evaluation, including
// every missing or incompatible capability. Executable summarizes whether the
// request can be executed against all of the targeted indexes.
type PreflightReport struct {
	// Executable is true when all the targeted indexes can serve every
	// clause of the request.
	Executable   bool                             `json:"executable"`
	Requirements []*ClauseRequirement             `json:"requirements"`
	Indexes      map[string]*IndexPreflightReport `json:"indexes"`
}

// Err returns nil when the report is executable, otherwise a
// *PreflightExecutionError describing the problems found across the indexes.
func (r *PreflightReport) Err() error {
	if r == nil || r.Executable {
		return nil
	}
	return &PreflightExecutionError{Report: r}
}

// IndexProblems returns the problems recorded for the named index, if any.
func (r *PreflightReport) IndexProblems(indexName string) []*CapabilityProblem {
	if ir, ok := r.Indexes[indexName]; ok {
		return ir.Problems
	}
	return nil
}

// PreflightExecutionError is returned (instead of running the search) when an
// actual search is dispatched with a preflight report attached and the report
// determines the request is not executable against the target index(es).
type PreflightExecutionError struct {
	Report *PreflightReport
}

func (e *PreflightExecutionError) Error() string {
	var names []string
	for name := range e.Report.Indexes {
		names = append(names, name)
	}
	sort.Strings(names)
	var b strings.Builder
	b.WriteString("query preflight failed")
	for _, name := range names {
		ir := e.Report.Indexes[name]
		if ir.OK {
			continue
		}
		fmt.Fprintf(&b, "\n  index %q:", name)
		for _, p := range ir.Problems {
			fmt.Fprintf(&b, "\n    - %s", p.Reason)
		}
	}
	return b.String()
}

// PreflightSearch performs an optional query capability preflight of the
// request against the index (or every leaf index of an index alias, including
// nested aliases). It does not execute the search and never opens an index
// reader; the request's default error semantics are unchanged.
//
// The returned report lists the capabilities required by each clause, the
// missing or incompatible capabilities found in each index and the overall
// executability conclusion.
func PreflightSearch(in Index, req *SearchRequest) (*PreflightReport, error) {
	return PreflightSearchInContext(context.Background(), in, req)
}

// PreflightSearchInContext is the context aware variant of PreflightSearch.
// The preflight itself performs no index IO, so the context is only carried
// for API symmetry.
func PreflightSearchInContext(ctx context.Context, in Index, req *SearchRequest) (*PreflightReport, error) {
	if in == nil {
		return nil, errors.New("preflight requires a non-nil index")
	}
	if req == nil {
		return nil, errors.New("preflight requires a non-nil search request")
	}
	if req.Query == nil {
		return nil, errors.New("preflight requires a non-nil query")
	}

	requirements, err := extractSearchRequirements(req)
	if err != nil {
		return nil, err
	}

	report := &PreflightReport{
		Executable:   true,
		Requirements: requirements,
		Indexes:      make(map[string]*IndexPreflightReport),
	}
	if err := preflightIndex(ctx, in, requirements, report); err != nil {
		return nil, err
	}
	if len(report.Indexes) == 0 {
		return nil, ErrorAliasEmpty
	}
	for _, ir := range report.Indexes {
		if !ir.OK {
			report.Executable = false
			break
		}
	}
	return report, nil
}

// SearchWithPreflight runs the preflight and, only when the request is
// executable against every targeted index, executes the search reusing the
// same preflight judgment. When the preflight fails, the search is not
// executed and the report is returned together with the error.
//
// Callers that do not want a preflight can keep using Search/SearchInContext
// directly; the default error semantics are unchanged.
func SearchWithPreflight(ctx context.Context, in Index, req *SearchRequest) (*SearchResult, *PreflightReport, error) {
	report, err := PreflightSearchInContext(ctx, in, req)
	if err != nil {
		return nil, nil, err
	}
	if !report.Executable {
		return nil, report, report.Err()
	}
	sr, err := in.SearchInContext(ContextWithPreflightReport(ctx, report), req)
	return sr, report, err
}

// preflightIndex evaluates the requirements against a single index or, for an
// alias, against every leaf index (recursively expanding nested aliases).
func preflightIndex(ctx context.Context, in Index, requirements []*ClauseRequirement, report *PreflightReport) error {
	if alias, ok := in.(*indexAliasImpl); ok {
		var visitErr error
		alias.VisitIndexes(func(child Index) {
			if visitErr == nil {
				visitErr = preflightIndex(ctx, child, requirements, report)
			}
		})
		return visitErr
	}

	m := in.Mapping()
	if m == nil {
		return fmt.Errorf("preflight: index %q does not provide a mapping", in.Name())
	}
	name := in.Name()
	problems := evaluateClauseRequirements(m, requirements)
	report.Indexes[name] = &IndexPreflightReport{
		Name:     name,
		OK:       len(problems) == 0,
		Problems: problems,
	}
	return nil
}

// extractSearchRequirements derives the index independent clause requirements
// of the request, covering the query tree (including nested boolean,
// conjunction, disjunction and query string clauses), the sort order and the
// kNN requests.
func extractSearchRequirements(req *SearchRequest) ([]*ClauseRequirement, error) {
	requirements := make([]*ClauseRequirement, 0)
	requirements, err := extractClauseRequirements(req.Query, "", requirements)
	if err != nil {
		return nil, err
	}
	requirements = appendSortRequirements(req, requirements)
	requirements, err = appendKNNClauseRequirements(req, requirements)
	if err != nil {
		return nil, err
	}
	return requirements, nil
}

// extractClauseRequirements walks a query tree and appends the field
// capability requirements of every clause, descending into nested compound
// clauses and expanding query string clauses.
func extractClauseRequirements(q query.Query, path string, requirements []*ClauseRequirement) ([]*ClauseRequirement, error) {
	if q == nil {
		return requirements, nil
	}
	switch q := q.(type) {
	case *query.QueryStringQuery:
		expanded, err := query.ExpandQuery(q)
		if err != nil {
			return nil, err
		}
		return extractClauseRequirements(expanded, path, requirements)
	case *query.BooleanQuery:
		var err error
		groups := []struct {
			name string
			sub  query.Query
		}{
			{"must", q.Must},
			{"should", q.Should},
			{"must_not", q.MustNot},
			{"filter", q.Filter},
		}
		for _, group := range groups {
			requirements, err = extractClauseRequirements(group.sub, joinClausePath(path, group.name), requirements)
			if err != nil {
				return nil, err
			}
		}
		return requirements, nil
	case *query.ConjunctionQuery:
		for i, sub := range q.Conjuncts {
			var err error
			requirements, err = extractClauseRequirements(sub, indexedClausePath(path, "conjuncts", i), requirements)
			if err != nil {
				return nil, err
			}
		}
		return requirements, nil
	case *query.DisjunctionQuery:
		for i, sub := range q.Disjuncts {
			var err error
			requirements, err = extractClauseRequirements(sub, indexedClausePath(path, "disjuncts", i), requirements)
			if err != nil {
				return nil, err
			}
		}
		return requirements, nil
	case *query.DocIDQuery, *query.MatchAllQuery, *query.MatchNoneQuery:
		return requirements, nil
	}

	clause, needs := clauseCapability(q)
	var dims int
	if clause == "" {
		// build specific clauses (for example a KNN query placed directly
		// in the query tree) are recognized through this hook
		clause, needs, dims = vectorClauseCapability(q)
	}
	if clause == "" {
		// clauses without a field (custom or unknown query types) have no
		// statically checkable field requirements
		return requirements, nil
	}
	fieldable, ok := q.(query.FieldableQuery)
	if !ok {
		return requirements, nil
	}
	requirements = append(requirements, &ClauseRequirement{
		Path:       path,
		Clause:     clause,
		Field:      fieldable.Field(),
		Needs:      needs,
		VectorDims: dims,
	})
	return requirements, nil
}

// vectorClauseCapability recognizes vector only leaf clauses. It is overridden
// through init() in vectors tagged builds.
var vectorClauseCapability = func(q query.Query) (string, FieldCapability, int) {
	return "", 0, 0
}

// clauseCapability maps a leaf query clause to its kind and required field
// capabilities. An empty clause kind means the query type does not reference a
// statically checkable field.
func clauseCapability(q query.Query) (string, FieldCapability) {
	switch q.(type) {
	case *query.PhraseQuery, *query.MultiPhraseQuery, *query.MatchPhraseQuery:
		return "phrase", CapabilityIndexed | CapabilityTermVectors
	case *query.MatchQuery:
		return "match", CapabilityIndexed
	case *query.TermQuery:
		return "term", CapabilityIndexed
	case *query.FuzzyQuery:
		return "fuzzy", CapabilityIndexed
	case *query.PrefixQuery:
		return "prefix", CapabilityIndexed
	case *query.RegexpQuery:
		return "regexp", CapabilityIndexed
	case *query.WildcardQuery:
		return "wildcard", CapabilityIndexed
	case *query.TermRangeQuery:
		return "term_range", CapabilityIndexed
	case *query.NumericRangeQuery:
		return "numeric_range", CapabilityIndexed
	case *query.DateRangeQuery, *query.DateRangeStringQuery:
		return "date_range", CapabilityIndexed
	case *query.BoolFieldQuery:
		return "bool_field", CapabilityIndexed
	case *query.GeoBoundingBoxQuery:
		return "geo_bounding_box", CapabilityIndexed
	case *query.GeoDistanceQuery:
		return "geo_distance", CapabilityIndexed
	case *query.GeoBoundingPolygonQuery:
		return "geo_bounding_polygon", CapabilityIndexed
	case *query.GeoShapeQuery, *query.GeoShapeV2Query:
		return "geo_shape", CapabilityIndexed
	case *query.IPRangeQuery:
		return "ip_range", CapabilityIndexed
	}
	return "", 0
}

// appendSortRequirements appends the field capability requirements implied by
// the request's sort order. Score and doc id sorts need no field capabilities.
func appendSortRequirements(req *SearchRequest, requirements []*ClauseRequirement) []*ClauseRequirement {
	for i, sortClause := range req.Sort {
		path := fmt.Sprintf("sort[%d]", i)
		switch s := sortClause.(type) {
		case *search.SortField:
			requirements = append(requirements, &ClauseRequirement{
				Path:   path,
				Clause: "sort",
				Field:  s.Field,
				Needs:  CapabilityDocValues,
			})
		case *search.SortGeoDistance:
			requirements = append(requirements, &ClauseRequirement{
				Path:   path,
				Clause: "sort_geo_distance",
				Field:  s.Field,
				Needs:  CapabilityDocValues | CapabilityIndexed,
			})
		}
	}
	return requirements
}

// evaluateClauseRequirements evaluates index independent clause requirements
// against a concrete index mapping and returns every missing or incompatible
// capability. This is the single judgment function reused by both the
// preflight API and enforced searches.
func evaluateClauseRequirements(m mapping.IndexMapping, requirements []*ClauseRequirement) []*CapabilityProblem {
	var problems []*CapabilityProblem
	for _, requirement := range requirements {
		field := requirement.Field
		if field == "" {
			field = m.DefaultSearchField()
			if field == "" {
				problems = append(problems, newProblem(requirement, field,
					PreflightReasonDefaultFieldMissing,
					fmt.Sprintf("%s clause at %q does not specify a field and the index mapping has no default search field",
						requirement.Clause, requirement.Path)))
				continue
			}
		}

		fieldMapping := m.FieldMappingForPath(field)
		if fieldMapping.Type == "" {
			problems = append(problems, newProblem(requirement, field,
				PreflightReasonFieldNotMapped,
				fmt.Sprintf("field '%s' required by %s clause at %q is not mapped in the index",
					field, requirement.Clause, requirement.Path)))
			continue
		}

		if requirement.Needs&CapabilityVector != 0 {
			if fieldMapping.Type != "vector" && fieldMapping.Type != "vector_base64" {
				problems = append(problems, newProblem(requirement, field,
					PreflightReasonNotVectorField,
					fmt.Sprintf("field '%s' required by %s clause at %q is mapped as '%s', not as a vector field",
						field, requirement.Clause, requirement.Path, fieldMapping.Type)))
				continue
			}
			if !fieldMapping.Index {
				problems = append(problems, newProblem(requirement, field,
					PreflightReasonNotIndexed,
					fmt.Sprintf("vector field '%s' required by %s clause at %q is not indexed",
						field, requirement.Clause, requirement.Path)))
			}
			if requirement.VectorDims != 0 && fieldMapping.Dims != requirement.VectorDims {
				problems = append(problems, newProblem(requirement, field,
					PreflightReasonVectorDimMismatch,
					fmt.Sprintf("vector field '%s' has dimensionality %d but %s clause at %q provides a vector with %d dimensions",
						field, fieldMapping.Dims, requirement.Clause, requirement.Path, requirement.VectorDims)))
			}
			continue
		}

		if requirement.Needs&CapabilityIndexed != 0 && !fieldMapping.Index {
			problems = append(problems, newProblem(requirement, field,
				PreflightReasonNotIndexed,
				fmt.Sprintf("field '%s' required by %s clause at %q is not indexed",
					field, requirement.Clause, requirement.Path)))
		}
		if requirement.Needs&CapabilityTermVectors != 0 && !fieldMapping.IncludeTermVectors {
			problems = append(problems, newProblem(requirement, field,
				PreflightReasonMissingTermVectors,
				fmt.Sprintf("field '%s' required by %s clause at %q does not include term vectors",
					field, requirement.Clause, requirement.Path)))
		}
		if requirement.Needs&CapabilityDocValues != 0 && !fieldMapping.DocValues {
			problems = append(problems, newProblem(requirement, field,
				PreflightReasonMissingDocValues,
				fmt.Sprintf("field '%s' required by %s clause at %q does not include doc values",
					field, requirement.Clause, requirement.Path)))
		}
	}
	return problems
}

func newProblem(requirement *ClauseRequirement, field, code, reason string) *CapabilityProblem {
	return &CapabilityProblem{
		Path:   requirement.Path,
		Clause: requirement.Clause,
		Field:  field,
		Needs:  requirement.Needs,
		Code:   code,
		Reason: reason,
	}
}

func joinClausePath(parent, name string) string {
	if parent == "" {
		return name
	}
	return parent + "." + name
}

func indexedClausePath(parent, name string, index int) string {
	return fmt.Sprintf("%s[%d]", joinClausePath(parent, name), index)
}

// preflightReportContextKey attaches a PreflightReport to a search context so
// that the actual search reuses the preflight judgment.
type preflightReportContextKey struct{}

// ContextWithPreflightReport returns a child context which makes the search
// execution reuse the given preflight report. When a search is dispatched with
// such a context, every targeted index validates itself against the report
// before any search work is done; a non executable report makes the search
// fail fast with a *PreflightExecutionError. Without such a context the
// default search behavior and error semantics are unchanged.
func ContextWithPreflightReport(ctx context.Context, report *PreflightReport) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, preflightReportContextKey{}, report)
}

func preflightReportFromContext(ctx context.Context) (*PreflightReport, bool) {
	if ctx == nil {
		return nil, false
	}
	report, ok := ctx.Value(preflightReportContextKey{}).(*PreflightReport)
	return report, ok
}

// enforcePreflight validates the (leaf) indexes against a preflight report
// carried by the context. It is a no-op when no report is attached, so direct
// searches keep their original behavior.
func enforcePreflight(ctx context.Context, indexes []Index) error {
	report, ok := preflightReportFromContext(ctx)
	if !ok || report == nil {
		return nil
	}
	for _, leaf := range collectLeafIndexes(indexes) {
		entry, found := report.Indexes[leaf.Name()]
		if !found {
			// the alias topology changed since the preflight was performed
			return ErrorTwoPhaseSearchInconsistency
		}
		if !entry.OK {
			return &PreflightExecutionError{Report: report}
		}
	}
	return nil
}

// collectLeafIndexes expands aliases (including nested aliases) into their
// leaf indexes. Non alias indexes are returned as-is.
func collectLeafIndexes(indexes []Index) []Index {
	var leaves []Index
	for _, in := range indexes {
		if alias, ok := in.(*indexAliasImpl); ok {
			leaves = append(leaves, alias.leafIndexes()...)
			continue
		}
		leaves = append(leaves, in)
	}
	return leaves
}
