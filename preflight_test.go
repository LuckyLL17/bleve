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
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/blevesearch/bleve/v2/search"
)

func newPreflightIndex(t *testing.T) Index {
	t.Helper()

	title := NewTextFieldMapping()

	body := NewTextFieldMapping()
	body.Store = false
	body.IncludeTermVectors = false
	body.DocValues = false

	dm := NewDocumentStaticMapping()
	dm.AddFieldMappingsAt("title", title)
	dm.AddFieldMappingsAt("body", body)
	dm.AddFieldMappingsAt("count", NewNumericFieldMapping())
	dm.AddFieldMappingsAt("when", NewDateTimeFieldMapping())
	dm.AddFieldMappingsAt("active", NewBooleanFieldMapping())
	dm.AddFieldMappingsAt("point", NewGeoPointFieldMapping())

	im := NewIndexMapping()
	im.DefaultMapping = dm

	idx, err := NewMemOnly(im)
	if err != nil {
		t.Fatal(err)
	}
	return idx
}

func preflight(t *testing.T, idx Index, req *SearchRequest) *PreflightReport {
	t.Helper()
	report, err := idx.PreflightSearchRequest(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	return report
}

func findIssue(issues []*PreflightIssue, section, field string) *PreflightIssue {
	for _, issue := range issues {
		if issue.Section == section && issue.Field == field {
			return issue
		}
	}
	return nil
}

func TestPreflightValidRequest(t *testing.T) {
	idx := newPreflightIndex(t)
	defer idx.Close()

	req := NewSearchRequest(NewMatchAllQuery())
	report := preflight(t, idx, req)
	if !report.Valid {
		t.Fatalf("expected valid report, got issues: %v", report.Issues)
	}
	if len(report.Issues) != 0 {
		t.Fatalf("expected no issues, got: %v", report.Issues)
	}
}

func TestPreflightNilQuery(t *testing.T) {
	idx := newPreflightIndex(t)
	defer idx.Close()

	req := NewSearchRequest(NewMatchAllQuery())
	req.Query = nil
	report := preflight(t, idx, req)
	if report.Valid {
		t.Fatal("expected invalid report")
	}
	issue := findIssue(report.Issues, PreflightSectionQuery, "")
	if issue == nil {
		t.Fatalf("expected query issue, got: %v", report.Issues)
	}
	if !issue.Fatal || issue.Message != "query is required" {
		t.Fatalf("unexpected issue: %+v", issue)
	}
}

func TestPreflightUnmappedField(t *testing.T) {
	idx := newPreflightIndex(t)
	defer idx.Close()

	q := NewTermQuery("x")
	q.SetField("ghost")
	req := NewSearchRequest(q)
	report := preflight(t, idx, req)
	if report.Valid {
		t.Fatal("expected invalid report")
	}
	issue := findIssue(report.Issues, PreflightSectionQuery, "ghost")
	if issue == nil {
		t.Fatalf("expected unmapped field issue, got: %v", report.Issues)
	}
	if issue.Fatal {
		t.Fatalf("expected advisory issue, got fatal: %+v", issue)
	}
}

func TestPreflightQueryFieldTypeMismatch(t *testing.T) {
	idx := newPreflightIndex(t)
	defer idx.Close()

	min, max := float64(0), float64(10)
	q := NewNumericRangeQuery(&min, &max)
	q.SetField("title")
	req := NewSearchRequest(q)
	report := preflight(t, idx, req)
	issue := findIssue(report.Issues, PreflightSectionQuery, "title")
	if issue == nil || issue.Fatal {
		t.Fatalf("expected advisory type mismatch issue, got: %v", report.Issues)
	}
}

func TestPreflightPhraseRequiresTermVectors(t *testing.T) {
	idx := newPreflightIndex(t)
	defer idx.Close()

	req := NewSearchRequest(NewPhraseQuery([]string{"word"}, "body"))
	report := preflight(t, idx, req)
	issue := findIssue(report.Issues, PreflightSectionQuery, "body")
	if issue == nil {
		t.Fatalf("expected term vectors issue, got: %v", report.Issues)
	}
	if issue.Fatal {
		t.Fatalf("expected advisory issue, got fatal: %+v", issue)
	}
}

func TestPreflightSortUnmapped(t *testing.T) {
	idx := newPreflightIndex(t)
	defer idx.Close()

	req := NewSearchRequest(NewMatchAllQuery())
	req.SortBy([]string{"ghost"})
	report := preflight(t, idx, req)
	issue := findIssue(report.Issues, PreflightSectionSort, "ghost")
	if issue == nil || issue.Fatal {
		t.Fatalf("expected advisory sort issue, got: %v", report.Issues)
	}
}

func TestPreflightSortNoDocValues(t *testing.T) {
	idx := newPreflightIndex(t)
	defer idx.Close()

	req := NewSearchRequest(NewMatchAllQuery())
	req.SortByCustom(search.SortOrder{
		&search.SortField{Field: "body"},
	})
	report := preflight(t, idx, req)
	issue := findIssue(report.Issues, PreflightSectionSort, "body")
	if issue == nil || issue.Fatal {
		t.Fatalf("expected advisory doc values issue, got: %v", report.Issues)
	}
}

func TestPreflightSortTypeMismatch(t *testing.T) {
	idx := newPreflightIndex(t)
	defer idx.Close()

	req := NewSearchRequest(NewMatchAllQuery())
	req.SortByCustom(search.SortOrder{
		&search.SortField{Field: "title", Type: search.SortFieldAsNumber},
	})
	report := preflight(t, idx, req)
	issue := findIssue(report.Issues, PreflightSectionSort, "title")
	if issue == nil || issue.Fatal {
		t.Fatalf("expected advisory sort type mismatch, got: %v", report.Issues)
	}
}

func TestPreflightGeoSortTypeMismatch(t *testing.T) {
	idx := newPreflightIndex(t)
	defer idx.Close()

	geoSort, err := search.NewSortGeoDistance("title", "km", 0, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	req := NewSearchRequest(NewMatchAllQuery())
	req.SortByCustom(search.SortOrder{geoSort})
	report := preflight(t, idx, req)
	issue := findIssue(report.Issues, PreflightSectionSort, "title")
	if issue == nil || issue.Fatal {
		t.Fatalf("expected advisory geo sort mismatch, got: %v", report.Issues)
	}
}

func TestPreflightFacetTypeMismatch(t *testing.T) {
	idx := newPreflightIndex(t)
	defer idx.Close()

	facetRequest := NewFacetRequest("title", 10)
	min, max := float64(0), float64(10)
	facetRequest.AddNumericRange("r", &min, &max)
	req := NewSearchRequest(NewMatchAllQuery())
	req.AddFacet("nf", facetRequest)

	report := preflight(t, idx, req)
	issue := findIssue(report.Issues, PreflightSectionFacet, "title")
	if issue == nil || issue.Fatal {
		t.Fatalf("expected advisory facet mismatch, got: %v", report.Issues)
	}
	if issue.Name != "nf" {
		t.Fatalf("expected facet name 'nf', got '%s'", issue.Name)
	}
}

func TestPreflightFacetMissingParser(t *testing.T) {
	idx := newPreflightIndex(t)
	defer idx.Close()

	facetRequest := NewFacetRequest("when", 10)
	start := "2020-01-01"
	facetRequest.AddDateTimeRangeStringWithParser("r", &start, nil, "bogusparser")
	req := NewSearchRequest(NewMatchAllQuery())
	req.AddFacet("df", facetRequest)

	report := preflight(t, idx, req)
	issue := findIssue(report.Issues, PreflightSectionFacet, "when")
	if issue == nil {
		t.Fatalf("expected missing parser issue, got: %v", report.Issues)
	}
	if !issue.Fatal {
		t.Fatalf("expected fatal parser issue, got: %+v", issue)
	}
}

func TestPreflightFacetNoDocValues(t *testing.T) {
	idx := newPreflightIndex(t)
	defer idx.Close()

	req := NewSearchRequest(NewMatchAllQuery())
	req.AddFacet("bf", NewFacetRequest("body", 10))

	report := preflight(t, idx, req)
	issue := findIssue(report.Issues, PreflightSectionFacet, "body")
	if issue == nil || issue.Fatal {
		t.Fatalf("expected advisory facet doc values issue, got: %v", report.Issues)
	}
}

func TestPreflightTermFacetOnNumber(t *testing.T) {
	idx := newPreflightIndex(t)
	defer idx.Close()

	req := NewSearchRequest(NewMatchAllQuery())
	req.AddFacet("cf", NewFacetRequest("count", 10))

	report := preflight(t, idx, req)
	issue := findIssue(report.Issues, PreflightSectionFacet, "count")
	if issue == nil || issue.Fatal {
		t.Fatalf("expected advisory term facet mismatch, got: %v", report.Issues)
	}
}

func TestPreflightHighlightUnknownStyle(t *testing.T) {
	idx := newPreflightIndex(t)
	defer idx.Close()

	req := NewSearchRequest(NewMatchAllQuery())
	req.Highlight = NewHighlightWithStyle("nosuchstyle")
	report := preflight(t, idx, req)
	issue := findIssue(report.Issues, PreflightSectionHighlight, "")
	if issue == nil {
		t.Fatalf("expected unknown highlighter issue, got: %v", report.Issues)
	}
	if !issue.Fatal {
		t.Fatalf("expected fatal highlight issue, got: %+v", issue)
	}
}

func TestPreflightHighlightMissingDeps(t *testing.T) {
	idx := newPreflightIndex(t)
	defer idx.Close()

	h := NewHighlight()
	h.AddField("body")
	req := NewSearchRequest(NewMatchAllQuery())
	req.Highlight = h

	report := preflight(t, idx, req)
	stored := findIssue(report.Issues, PreflightSectionHighlight, "body")
	if stored == nil || stored.Fatal {
		t.Fatalf("expected advisory highlight dependency issues, got: %v",
			report.Issues)
	}
	if len(report.Issues) < 2 {
		t.Fatalf("expected store and term vector issues, got: %v", report.Issues)
	}
}

func TestPreflightExecutionGate(t *testing.T) {
	idx := newPreflightIndex(t)
	defer idx.Close()

	req := NewSearchRequest(NewMatchAllQuery())
	req.Highlight = NewHighlightWithStyle("nosuchstyle")
	_, err := idx.Search(req)
	var preflightErr *PreflightError
	if !errors.As(err, &preflightErr) {
		t.Fatalf("expected PreflightError, got: %v", err)
	}
}

func TestPreflightSuccessfulSearchUnchanged(t *testing.T) {
	idx := newPreflightIndex(t)
	defer idx.Close()

	if err := idx.Index("d1", map[string]interface{}{
		"title": "hello world",
	}); err != nil {
		t.Fatal(err)
	}
	mq := NewMatchQuery("hello")
	mq.SetField("title")
	req := NewSearchRequest(mq)
	result, err := idx.Search(req)
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 1 || len(result.Hits) != 1 || result.Hits[0].ID != "d1" {
		t.Fatalf("unexpected search result: %s", result)
	}
}

func TestPreflightDisjunctionAdvisory(t *testing.T) {
	idx := newPreflightIndex(t)
	defer idx.Close()

	dq := NewDisjunctionQuery(NewMatchAllQuery())
	dq.SetMin(2)
	req := NewSearchRequest(dq)
	report := preflight(t, idx, req)
	issue := findIssue(report.Issues, PreflightSectionQuery, "")
	if issue == nil {
		t.Fatalf("expected disjunction issue, got: %v", report.Issues)
	}
	if issue.Fatal {
		t.Fatalf("expected advisory disjunction issue, got fatal: %+v", issue)
	}
	result, err := idx.Search(req)
	if err != nil {
		t.Fatalf("expected execution to tolerate disjunction, got: %v", err)
	}
	if result.Total != 0 {
		t.Fatalf("expected 0 matches, got: %d", result.Total)
	}
}

func newNamedPreflightIndex(t *testing.T, name string) Index {
	idx := newPreflightIndex(t)
	idx.SetName(name)
	return idx
}

func TestPreflightAliasMembers(t *testing.T) {
	idx1 := newNamedPreflightIndex(t, "i1")
	defer idx1.Close()
	idx2 := newNamedPreflightIndex(t, "i2")
	defer idx2.Close()

	alias := NewIndexAlias(idx1, idx2)

	q := NewTermQuery("x")
	q.SetField("ghost")
	req := NewSearchRequest(q)

	report := preflight(t, alias, req)
	if report.Valid {
		t.Fatal("expected invalid alias report")
	}
	if len(report.IndexIssues["i1"]) == 0 || len(report.IndexIssues["i2"]) == 0 {
		t.Fatalf("expected per-member issues, got: %v", report.IndexIssues)
	}
	if findIssue(report.IndexIssues["i1"], PreflightSectionQuery, "ghost") == nil {
		t.Fatalf("expected ghost issue for i1, got: %v", report.IndexIssues["i1"])
	}
	if findIssue(report.IndexIssues["i2"], PreflightSectionQuery, "ghost") == nil {
		t.Fatalf("expected ghost issue for i2, got: %v", report.IndexIssues["i2"])
	}
}

func TestPreflightAliasValid(t *testing.T) {
	idx1 := newNamedPreflightIndex(t, "i1")
	defer idx1.Close()
	idx2 := newNamedPreflightIndex(t, "i2")
	defer idx2.Close()

	alias := NewIndexAlias(idx1, idx2)
	report := preflight(t, alias, NewSearchRequest(NewMatchAllQuery()))
	if !report.Valid {
		t.Fatalf("expected valid alias report, got: %v", report)
	}
}

func TestPreflightNestedAliasFlattened(t *testing.T) {
	idx1 := newNamedPreflightIndex(t, "i1")
	defer idx1.Close()
	idx2 := newNamedPreflightIndex(t, "i2")
	defer idx2.Close()

	inner := NewIndexAlias(idx2)
	inner.SetName("inner")
	outer := NewIndexAlias(idx1, inner)

	q := NewTermQuery("x")
	q.SetField("ghost")
	req := NewSearchRequest(q)
	report := preflight(t, outer, req)
	if _, ok := report.IndexIssues["i2"]; !ok {
		t.Fatalf("expected flattened i2 issues, got: %v", report.IndexIssues)
	}
	if _, ok := report.IndexIssues["i1"]; !ok {
		t.Fatalf("expected i1 issues, got: %v", report.IndexIssues)
	}
}

func TestPreflightAliasNilQuery(t *testing.T) {
	idx1 := newNamedPreflightIndex(t, "i1")
	defer idx1.Close()
	idx2 := newNamedPreflightIndex(t, "i2")
	defer idx2.Close()

	alias := NewIndexAlias(idx1, idx2)
	req := NewSearchRequest(NewMatchAllQuery())
	req.Query = nil

	report := preflight(t, alias, req)
	issue := findIssue(report.Issues, PreflightSectionQuery, "")
	if issue == nil || !issue.Fatal {
		t.Fatalf("expected fatal alias query issue, got: %v", report.Issues)
	}

	_, err := alias.Search(req)
	var preflightErr *PreflightError
	if !errors.As(err, &preflightErr) {
		t.Fatalf("expected PreflightError from alias search, got: %v", err)
	}
}

func TestPreflightSortSizeMismatch(t *testing.T) {
	idx := newPreflightIndex(t)
	defer idx.Close()

	req := NewSearchRequest(NewMatchAllQuery())
	req.Sort = search.SortOrder{}
	req.SetSearchAfter([]string{"x"})
	if err := req.Validate(); err == nil {
		t.Fatal("expected Validate error for size mismatch")
	}
	report := preflight(t, idx, req)
	issue := findIssue(report.Issues, PreflightSectionSort, "")
	if issue == nil || issue.Fatal {
		t.Fatalf("expected advisory sort size mismatch, got: %v", report.Issues)
	}
	// execution tolerates the malformed cursor; it must not panic
	if _, err := idx.Search(req); err != nil {
		t.Fatalf("expected lenient execution, got: %v", err)
	}
}

func TestPreflightReportJSON(t *testing.T) {
	idx := newPreflightIndex(t)
	defer idx.Close()

	req := NewSearchRequest(NewMatchAllQuery())
	req.Query = nil
	report := preflight(t, idx, req)

	data, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	var decoded PreflightReport
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Valid {
		t.Fatalf("expected invalid decoded report, got: %s", data)
	}
	if len(decoded.Issues) != 1 || decoded.Issues[0].Section != PreflightSectionQuery {
		t.Fatalf("unexpected decoded report: %s", data)
	}
}
