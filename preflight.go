//  Copyright (c) 2026 Couchbase, Inc.
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
	"fmt"
	"reflect"
	"strings"

	"github.com/blevesearch/bleve/v2/mapping"
	"github.com/blevesearch/bleve/v2/search"
	"github.com/blevesearch/bleve/v2/search/query"
	index "github.com/blevesearch/bleve_index_api"
)

const (
	PreflightSectionQuery     = "query"
	PreflightSectionSort      = "sort"
	PreflightSectionFacet     = "facet"
	PreflightSectionHighlight = "highlight"
	PreflightSectionKNN       = "knn"
)

// PreflightIssue describes a single diagnostic located within a specific
// section of the SearchRequest.
//
// Section is one of the PreflightSection* constants.
// Field is the mapped index field the issue refers to, when applicable.
// Name identifies the request element (for example the facet name or the
// KNN clause position).
// Fatal reports whether executing the request would reject the clause;
// non-fatal issues describe clauses that execute today but cannot produce
// meaningful values (empty matches, missing sort values and so on).
type PreflightIssue struct {
	Section string `json:"section"`
	Field   string `json:"field,omitempty"`
	Name    string `json:"name,omitempty"`
	Fatal   bool   `json:"fatal,omitempty"`
	Message string `json:"message"`
}

func (i *PreflightIssue) Error() string {
	return i.Message
}

// PreflightError aggregates the fatal issues found while compiling a
// SearchRequest and is returned by the execution paths when the request
// cannot be executed.
type PreflightError struct {
	Issues []*PreflightIssue `json:"issues"`
}

func (e *PreflightError) Error() string {
	messages := make([]string, 0, len(e.Issues))
	for _, issue := range e.Issues {
		messages = append(messages, issue.Message)
	}
	return strings.Join(messages, "; ")
}

// PreflightReport is the structured result of compiling a SearchRequest
// against an index or an index alias.
//
// Valid is true only when no issue was found.
// Issues holds the diagnostics for a single index.
// IndexIssues holds per-member diagnostics keyed by the member index name
// when the request was compiled against an alias.
// IndexErrors holds non-diagnostic per-member errors (for example a member
// index that could not open a reader).
type PreflightReport struct {
	Valid       bool                         `json:"valid"`
	Issues      []*PreflightIssue            `json:"issues,omitempty"`
	IndexIssues map[string][]*PreflightIssue `json:"index_issues,omitempty"`
	IndexErrors IndexErrMap                  `json:"index_errors,omitempty"`
}

func (r *PreflightReport) hasIssue() bool {
	if len(r.Issues) > 0 {
		return true
	}
	for _, issues := range r.IndexIssues {
		if len(issues) > 0 {
			return true
		}
	}
	return false
}

type requestCompiler struct {
	req    *SearchRequest
	m      mapping.IndexMapping
	reader index.IndexReader
	// strict selects the Validate-level judgments (reject structural
	// issues that execution otherwise tolerates)
	strict bool
	issues []*PreflightIssue
}

func newRequestCompiler(req *SearchRequest, m mapping.IndexMapping,
	reader index.IndexReader) *requestCompiler {
	return &requestCompiler{
		req:    req,
		m:      m,
		reader: reader,
	}
}

func (c *requestCompiler) add(fatal bool, section, field, name, message string) {
	c.issues = append(c.issues, &PreflightIssue{
		Section: section,
		Field:   field,
		Name:    name,
		Fatal:   fatal,
		Message: message,
	})
}

func (c *requestCompiler) compile() {
	c.compileQuery()
	c.compileSort()
	c.compileFacets()
	c.compileHighlight()
	c.compileKNN()
}

// resolveField returns the field mapping and whether the field can be
// addressed at execution. A field absent from the explicit mappings is
// still addressable when dynamic mapping is enabled.
func (c *requestCompiler) resolveField(field string) (mapping.FieldMapping, bool) {
	fm := c.m.FieldMappingForPath(field)
	if fm.Type != "" {
		return fm, true
	}
	return fm, c.dynamic()
}

func (c *requestCompiler) dynamic() bool {
	im, ok := c.m.(*mapping.IndexMappingImpl)
	if !ok || im.DefaultMapping == nil {
		return false
	}
	return im.DefaultMapping.Dynamic
}

func (c *requestCompiler) fieldName(fq query.FieldableQuery) string {
	field := fq.Field()
	if field == "" {
		field = c.m.DefaultSearchField()
	}
	return field
}

func (c *requestCompiler) compileQuery() {
	q := c.req.Query
	if q == nil {
		c.add(true, PreflightSectionQuery, "", "", "query is required")
		return
	}
	if vq, ok := q.(query.ValidatableQuery); ok {
		if err := vq.Validate(); err != nil {
			c.add(c.strict, PreflightSectionQuery, queryTopField(q), "", err.Error())
		}
	}
	if c.m == nil {
		return
	}
	fs, err := query.ExtractFields(q, c.m, nil)
	if err != nil {
		c.add(true, PreflightSectionQuery, "", "", err.Error())
		return
	}
	for field := range c.phraseFields(q) {
		fm, mapped := c.resolveField(field)
		if !mapped {
			continue
		}
		if !fm.IncludeTermVectors {
			c.add(false, PreflightSectionQuery, field, "",
				fmt.Sprintf("phrase query on field '%s' requires term vectors", field))
		}
	}
	for field := range fs {
		if field == "_id" {
			continue
		}
		fm, mapped := c.resolveField(field)
		if !mapped {
			c.add(false, PreflightSectionQuery, field, "",
				fmt.Sprintf("field '%s' is not mapped", field))
			continue
		}
		if fm.Type == "" {
			// dynamically addressed field: capabilities cannot be checked
			// without concrete values
			continue
		}
		if !fm.Index {
			c.add(false, PreflightSectionQuery, field, "",
				fmt.Sprintf("field '%s' is not indexed", field))
		}
	}
	visitFieldableQueries(q, func(fq query.FieldableQuery) {
		c.checkQueryFieldType(fq)
	})
}

func (c *requestCompiler) checkQueryFieldType(fq query.FieldableQuery) {
	expected, ok := expectedQueryFieldType(fq)
	if !ok {
		return
	}
	field := c.fieldName(fq)
	if field == "" {
		return
	}
	fm, mapped := c.resolveField(field)
	if !mapped || fm.Type == "" {
		return
	}
	if fieldTypeCompatible(expected, fm.Type) {
		return
	}
	c.add(false, PreflightSectionQuery, field, "",
		fmt.Sprintf("%s requires field '%s' to be a %s, mapped as '%s'",
			queryClauseName(fq), field, expected, fm.Type))
}

func expectedQueryFieldType(fq query.FieldableQuery) (string, bool) {
	switch fq.(type) {
	case *query.NumericRangeQuery:
		return "number", true
	case *query.DateRangeQuery, *query.DateRangeStringQuery:
		return "datetime", true
	case *query.GeoDistanceQuery, *query.GeoBoundingBoxQuery,
		*query.GeoBoundingPolygonQuery:
		return "geopoint", true
	case *query.GeoShapeQuery:
		return "geoshape", true
	case *query.GeoShapeV2Query:
		return "geoshape_v2", true
	case *query.BoolFieldQuery:
		return "boolean", true
	case *query.IPRangeQuery:
		return "IP", true
	case *query.TermQuery, *query.FuzzyQuery, *query.PrefixQuery,
		*query.RegexpQuery, *query.WildcardQuery, *query.TermRangeQuery,
		*query.MatchQuery:
		return "text", true
	}
	return "", false
}

func fieldTypeCompatible(expected, actual string) bool {
	if expected == actual {
		return true
	}
	if expected == "geoshape" {
		return actual == "geoshape_v2"
	}
	return false
}

func queryClauseName(fq query.FieldableQuery) string {
	switch fq.(type) {
	case *query.NumericRangeQuery:
		return "numeric range query"
	case *query.DateRangeQuery, *query.DateRangeStringQuery:
		return "date range query"
	case *query.GeoDistanceQuery:
		return "geo distance query"
	case *query.GeoBoundingBoxQuery:
		return "geo bounding box query"
	case *query.GeoBoundingPolygonQuery:
		return "geo bounding polygon query"
	case *query.GeoShapeQuery:
		return "geo shape query"
	case *query.GeoShapeV2Query:
		return "geo shape v2 query"
	case *query.BoolFieldQuery:
		return "bool field query"
	case *query.IPRangeQuery:
		return "ip range query"
	case *query.TermQuery:
		return "term query"
	case *query.FuzzyQuery:
		return "fuzzy query"
	case *query.PrefixQuery:
		return "prefix query"
	case *query.RegexpQuery:
		return "regexp query"
	case *query.WildcardQuery:
		return "wildcard query"
	case *query.TermRangeQuery:
		return "term range query"
	case *query.MatchQuery:
		return "match query"
	}
	return "query"
}

func queryTopField(q query.Query) string {
	if fq, ok := q.(query.FieldableQuery); ok {
		return fq.Field()
	}
	return ""
}

// visitFieldableQueries invokes visit on every field-referencing leaf
// clause within the query tree.
func visitFieldableQueries(q query.Query, visit func(query.FieldableQuery)) {
	switch q := q.(type) {
	case *query.BooleanQuery:
		visitFieldableQueries(q.Must, visit)
		visitFieldableQueries(q.Should, visit)
		visitFieldableQueries(q.MustNot, visit)
		visitFieldableQueries(q.Filter, visit)
	case *query.ConjunctionQuery:
		for _, subq := range q.Conjuncts {
			visitFieldableQueries(subq, visit)
		}
	case *query.DisjunctionQuery:
		for _, subq := range q.Disjuncts {
			visitFieldableQueries(subq, visit)
		}
	case *query.CustomScoreQuery:
		visitFieldableQueries(q.Query, visit)
	case *query.CustomFilterQuery:
		visitFieldableQueries(q.Query, visit)
	default:
		if fq, ok := q.(query.FieldableQuery); ok {
			visit(fq)
		}
	}
}

// phraseFields returns the fields referenced by phrase-style clauses,
// which require term vectors to locate matching positions.
func (c *requestCompiler) phraseFields(q query.Query) map[string]struct{} {
	rv := map[string]struct{}{}
	var collect func(query.Query)
	collect = func(q query.Query) {
		switch q := q.(type) {
		case *query.PhraseQuery, *query.MatchPhraseQuery, *query.MultiPhraseQuery:
			if fq, ok := q.(query.FieldableQuery); ok {
				field := c.fieldName(fq)
				if field != "" {
					rv[field] = struct{}{}
				}
			}
		case *query.BooleanQuery:
			collect(q.Must)
			collect(q.Should)
			collect(q.MustNot)
			collect(q.Filter)
		case *query.ConjunctionQuery:
			for _, subq := range q.Conjuncts {
				collect(subq)
			}
		case *query.DisjunctionQuery:
			for _, subq := range q.Disjuncts {
				collect(subq)
			}
		case *query.CustomScoreQuery:
			collect(q.Query)
		case *query.CustomFilterQuery:
			collect(q.Query)
		}
	}
	collect(q)
	return rv
}

func (c *requestCompiler) compileSort() {
	r := c.req
	if r.SearchAfter != nil && r.SearchBefore != nil {
		c.add(c.strict, PreflightSectionSort, "", "",
			"cannot use search after and search before together")
	}
	paginationOK := true
	if r.SearchAfter != nil {
		if r.From != 0 {
			c.add(c.strict, PreflightSectionSort, "", "",
				"cannot use search after with from !=0")
		}
		if len(r.SearchAfter) != len(r.Sort) {
			c.add(c.strict, PreflightSectionSort, "", "",
				"search after must have same size as sort order")
			paginationOK = false
		}
	}
	if r.SearchBefore != nil {
		if r.From != 0 {
			c.add(c.strict, PreflightSectionSort, "", "",
				"cannot use search before with from !=0")
		}
		if len(r.SearchBefore) != len(r.Sort) {
			c.add(c.strict, PreflightSectionSort, "", "",
				"search before must have same size as sort order")
			paginationOK = false
		}
	}
	if paginationOK {
		if err := r.validatePagination(); err != nil {
			c.add(c.strict, PreflightSectionSort, "", "", err.Error())
		}
	}
	if IsScoreFusionRequested(r) {
		if r.SearchAfter != nil || r.SearchBefore != nil {
			c.add(c.strict, PreflightSectionSort, "", "",
				"cannot use search after or search before with score fusion")
		}
		if r.Sort != nil && !reflect.DeepEqual(r.Sort, AllowedFusionSort) {
			c.add(c.strict, PreflightSectionSort, "", "",
				"sort must be empty or descending order of score for score fusion")
		}
	}
	if c.m == nil || r.Sort == nil {
		return
	}
	for _, soi := range r.Sort {
		switch s := soi.(type) {
		case *search.SortField:
			c.checkSortField(s.Field, s.Type)
		case *search.SortGeoDistance:
			c.checkGeoSortField(s.Field)
		}
	}
}

func (c *requestCompiler) checkSortField(field string, typ search.SortFieldType) {
	fm, mapped := c.resolveField(field)
	if !mapped {
		c.add(false, PreflightSectionSort, field, "",
			fmt.Sprintf("sort field '%s' is not mapped", field))
		return
	}
	if fm.Type == "" {
		return
	}
	if !fm.DocValues {
		c.add(false, PreflightSectionSort, field, "",
			fmt.Sprintf("sort field '%s' has no doc values", field))
	}
	switch typ {
	case search.SortFieldAsNumber:
		if fm.Type != "number" {
			c.add(false, PreflightSectionSort, field, "",
				fmt.Sprintf("numeric sort requires field '%s' to be a number, mapped as '%s'",
					field, fm.Type))
		}
	case search.SortFieldAsDate:
		if fm.Type != "datetime" {
			c.add(false, PreflightSectionSort, field, "",
				fmt.Sprintf("date sort requires field '%s' to be a datetime, mapped as '%s'",
					field, fm.Type))
		}
	case search.SortFieldAsString:
		if fm.Type == "number" || fm.Type == "datetime" {
			c.add(false, PreflightSectionSort, field, "",
				fmt.Sprintf("string sort on field '%s' of type '%s' has no usable values",
					field, fm.Type))
		}
	}
}

func (c *requestCompiler) checkGeoSortField(field string) {
	fm, mapped := c.resolveField(field)
	if !mapped {
		c.add(false, PreflightSectionSort, field, "",
			fmt.Sprintf("sort field '%s' is not mapped", field))
		return
	}
	if fm.Type == "" {
		return
	}
	if !fm.DocValues {
		c.add(false, PreflightSectionSort, field, "",
			fmt.Sprintf("sort field '%s' has no doc values", field))
	}
	if fm.Type != "geopoint" {
		c.add(false, PreflightSectionSort, field, "",
			fmt.Sprintf("geo distance sort requires field '%s' to be a geopoint, mapped as '%s'",
				field, fm.Type))
	}
}

func (c *requestCompiler) compileFacets() {
	for name, fr := range c.req.Facets {
		if err := fr.Validate(); err != nil {
			c.add(c.strict, PreflightSectionFacet, fr.Field, name, err.Error())
		}
		if c.m == nil {
			continue
		}
		fm, mapped := c.resolveField(fr.Field)
		if !mapped {
			c.add(false, PreflightSectionFacet, fr.Field, name,
				fmt.Sprintf("facet field '%s' is not mapped", fr.Field))
			continue
		}
		if fm.Type == "" {
			continue
		}
		if !fm.DocValues {
			c.add(false, PreflightSectionFacet, fr.Field, name,
				fmt.Sprintf("facet field '%s' has no doc values, facet cannot be computed",
					fr.Field))
		}
		switch {
		case len(fr.NumericRanges) > 0:
			if fm.Type != "number" {
				c.add(false, PreflightSectionFacet, fr.Field, name,
					fmt.Sprintf("numeric ranges facet requires field '%s' to be a number, mapped as '%s'",
						fr.Field, fm.Type))
			}
		case len(fr.DateTimeRanges) > 0:
			if fm.Type != "datetime" {
				c.add(false, PreflightSectionFacet, fr.Field, name,
					fmt.Sprintf("date ranges facet requires field '%s' to be a datetime, mapped as '%s'",
						fr.Field, fm.Type))
			}
			c.checkFacetDateParsers(fr)
		default:
			if fm.Type == "number" || fm.Type == "datetime" {
				c.add(false, PreflightSectionFacet, fr.Field, name,
					fmt.Sprintf("term facet on field '%s' of type '%s' has no usable values, use a ranges facet instead",
						fr.Field, fm.Type))
			}
		}
	}
}

func (c *requestCompiler) checkFacetDateParsers(fr *FacetRequest) {
	for _, dr := range fr.DateTimeRanges {
		parserName := defaultDateTimeParser
		if dr.DateTimeParser != "" {
			parserName = dr.DateTimeParser
		}
		if c.m.DateTimeParserNamed(parserName) == nil {
			c.add(true, PreflightSectionFacet, fr.Field, "",
				fmt.Sprintf("no date time parser named `%s` registered", parserName))
		}
	}
}

func (c *requestCompiler) compileHighlight() {
	h := c.req.Highlight
	if h == nil || c.m == nil {
		return
	}
	style := Config.DefaultHighlighter
	if h.Style != nil {
		style = *h.Style
	}
	highlighter, err := Config.Cache.HighlighterNamed(style)
	if err != nil {
		c.add(true, PreflightSectionHighlight, "", "", err.Error())
		return
	}
	if highlighter == nil {
		c.add(true, PreflightSectionHighlight, "", "",
			fmt.Sprintf("no highlighter named `%s` registered", style))
		return
	}
	if h.Fields != nil {
		for _, field := range h.Fields {
			c.checkHighlightField(field)
		}
		return
	}
	fs, err := query.ExtractFields(c.req.Query, c.m, nil)
	if err != nil {
		c.add(true, PreflightSectionHighlight, "", "", err.Error())
		return
	}
	for field := range fs {
		if field == "_id" {
			continue
		}
		c.checkHighlightField(field)
	}
}

func (c *requestCompiler) checkHighlightField(field string) {
	fm, mapped := c.resolveField(field)
	if !mapped {
		c.add(false, PreflightSectionHighlight, field, "",
			fmt.Sprintf("highlight field '%s' is not mapped", field))
		return
	}
	if fm.Type == "" {
		return
	}
	if fm.Type != "text" {
		c.add(false, PreflightSectionHighlight, field, "",
			fmt.Sprintf("highlight requires field '%s' to be a text field, mapped as '%s'",
				field, fm.Type))
		return
	}
	if !fm.Store {
		c.add(false, PreflightSectionHighlight, field, "",
			fmt.Sprintf("highlight field '%s' is not stored", field))
	}
	if !fm.IncludeTermVectors {
		c.add(false, PreflightSectionHighlight, field, "",
			fmt.Sprintf("highlight field '%s' has no term vectors", field))
	}
}

func fatalIssuesError(issues []*PreflightIssue) error {
	var fatal []*PreflightIssue
	for _, issue := range issues {
		if issue.Fatal {
			fatal = append(fatal, issue)
		}
	}
	if len(fatal) == 0 {
		return nil
	}
	return &PreflightError{Issues: fatal}
}
