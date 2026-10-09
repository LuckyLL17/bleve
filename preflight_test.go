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
	"testing"

	"github.com/blevesearch/bleve/v2/mapping"
	"github.com/blevesearch/bleve/v2/search"
	"github.com/blevesearch/bleve/v2/search/query"
)

func newPreflightTestIndex(t *testing.T, name string, fields map[string]*mapping.FieldMapping, defaultField string) Index {
	t.Helper()
	im := mapping.NewIndexMapping()
	im.DefaultMapping.Dynamic = false
	for field, fm := range fields {
		im.DefaultMapping.AddFieldMappingsAt(field, fm)
	}
	im.DefaultField = defaultField
	idx, err := NewMemOnly(im)
	if err != nil {
		t.Fatalf("error creating mem index: %v", err)
	}
	idx.SetName(name)
	return idx
}

func preflightFullTextField() *mapping.FieldMapping {
	return mapping.NewTextFieldMapping() // indexed, term vectors, doc values
}

func preflightTextNoTermVectorsNoDocValues() *mapping.FieldMapping {
	fm := mapping.NewTextFieldMapping()
	fm.IncludeTermVectors = false
	fm.DocValues = false
	return fm
}

func preflightUnindexedField() *mapping.FieldMapping {
	fm := mapping.NewTextFieldMapping()
	fm.Index = false
	return fm
}

func preflightFindProblem(t *testing.T, report *PreflightReport, indexName, path, code string) *CapabilityProblem {
	t.Helper()
	for _, p := range report.IndexProblems(indexName) {
		if p.Path == path && p.Code == code {
			return p
		}
	}
	t.Errorf("expected problem (path=%q, code=%q) for index %q, got: %v",
		path, code, indexName, report.IndexProblems(indexName))
	return nil
}

func TestPreflightTermFieldNotIndexed(t *testing.T) {
	idx := newPreflightTestIndex(t, "idx", map[string]*mapping.FieldMapping{
		"body":   preflightFullTextField(),
		"secret": preflightUnindexedField(),
	}, "")
	defer func() { _ = idx.Close() }()

	tq := NewTermQuery("value")
	tq.SetField("secret")
	req := NewSearchRequest(tq)

	report, err := PreflightSearch(idx, req)
	if err != nil {
		t.Fatalf("preflight returned error: %v", err)
	}
	if report.Executable {
		t.Errorf("expected report to be non-executable")
	}
	if report.Indexes["idx"] == nil || report.Indexes["idx"].OK {
		t.Fatalf("expected index idx to fail evaluation, got %+v", report.Indexes["idx"])
	}
	p := preflightFindProblem(t, report, "idx", "", PreflightReasonNotIndexed)
	if p != nil && p.Field != "secret" {
		t.Errorf("expected problem field secret, got %s", p.Field)
	}

	// direct search semantics are unchanged: no preflight attachment means
	// the request simply yields no matches instead of a preflight error
	sr, err := idx.SearchInContext(context.Background(), req)
	if err != nil {
		t.Fatalf("direct search should keep default semantics, got error: %v", err)
	}
	if sr.Total != 0 {
		t.Errorf("expected no matches on unindexed field, got %d", sr.Total)
	}
}

func TestPreflightPhraseRequiresTermVectors(t *testing.T) {
	idx := newPreflightTestIndex(t, "idx", map[string]*mapping.FieldMapping{
		"body":  preflightFullTextField(),
		"title": preflightTextNoTermVectorsNoDocValues(),
	}, "")
	defer func() { _ = idx.Close() }()

	mpq := NewMatchPhraseQuery("quick brown")
	mpq.SetField("title")
	report, err := PreflightSearch(idx, NewSearchRequest(mpq))
	if err != nil {
		t.Fatalf("preflight returned error: %v", err)
	}
	if report.Executable {
		t.Errorf("match phrase on field without term vectors must not be executable")
	}
	preflightFindProblem(t, report, "idx", "", PreflightReasonMissingTermVectors)

	pq := NewPhraseQuery([]string{"quick", "brown"}, "body")
	report, err = PreflightSearch(idx, NewSearchRequest(pq))
	if err != nil {
		t.Fatalf("preflight returned error: %v", err)
	}
	if !report.Executable {
		t.Errorf("phrase on body (term vectors enabled) must be executable, got %v", report.Indexes["idx"].Problems)
	}

	// direct search on the misconfigured field still uses default semantics
	sr, err := idx.Search(NewSearchRequest(mpq))
	if err != nil {
		t.Fatalf("direct phrase search should keep default semantics, got error: %v", err)
	}
	if sr.Total != 0 {
		t.Errorf("expected no phrase matches without term vectors, got %d", sr.Total)
	}
}

func TestPreflightSortRequiresDocValues(t *testing.T) {
	idx := newPreflightTestIndex(t, "idx", map[string]*mapping.FieldMapping{
		"body":  preflightFullTextField(),
		"title": preflightTextNoTermVectorsNoDocValues(),
	}, "")
	defer func() { _ = idx.Close() }()

	req := NewSearchRequest(NewMatchAllQuery())
	req.SortBy([]string{"-title"})

	report, err := PreflightSearch(idx, req)
	if err != nil {
		t.Fatalf("preflight returned error: %v", err)
	}
	if report.Executable {
		t.Errorf("sort on field without doc values must not be executable")
	}
	p := preflightFindProblem(t, report, "idx", "sort[0]", PreflightReasonMissingDocValues)
	if p != nil && p.Clause != "sort" {
		t.Errorf("expected sort clause, got %s", p.Clause)
	}

	reqOK := NewSearchRequest(NewMatchAllQuery())
	reqOK.SortBy([]string{"body"})
	report, err = PreflightSearch(idx, reqOK)
	if err != nil {
		t.Fatalf("preflight returned error: %v", err)
	}
	if !report.Executable {
		t.Errorf("sort on field with doc values must be executable, got %v", report.Indexes["idx"].Problems)
	}
}

func TestPreflightNestedCombinations(t *testing.T) {
	idx := newPreflightTestIndex(t, "idx", map[string]*mapping.FieldMapping{
		"body":   preflightFullTextField(),
		"title":  preflightTextNoTermVectorsNoDocValues(),
		"secret": preflightUnindexedField(),
	}, "")
	defer func() { _ = idx.Close() }()

	bodyTerm := func(term string) *query.TermQuery {
		q := NewTermQuery(term)
		q.SetField("body")
		return q
	}

	bq := NewBooleanQuery()
	bq.AddMust(bodyTerm("a"), NewPhraseQuery([]string{"b", "c"}, "title"))
	mq := NewMatchQuery("d")
	mq.SetField("body")
	bq.AddShould(mq)
	mustNot := NewTermQuery("e")
	mustNot.SetField("secret")
	bq.AddMustNot(mustNot)
	filter := NewTermQuery("f")
	filter.SetField("secret")
	bq.AddFilter(filter)

	report, err := PreflightSearch(idx, NewSearchRequest(bq))
	if err != nil {
		t.Fatalf("preflight returned error: %v", err)
	}
	if report.Executable {
		t.Errorf("nested query with incompatible leaves must not be executable")
	}

	expected := map[string]string{
		"must.conjuncts[0]":     "", // term on body: no problem
		"must.conjuncts[1]":     PreflightReasonMissingTermVectors,
		"should.disjuncts[0]":   "", // match on body: no problem
		"must_not.disjuncts[0]": PreflightReasonNotIndexed,
		"filter":                PreflightReasonNotIndexed,
	}
	problemsByPath := map[string]string{}
	for _, p := range report.Indexes["idx"].Problems {
		problemsByPath[p.Path] = p.Code
	}
	for path, code := range expected {
		if code == "" {
			if _, ok := problemsByPath[path]; ok {
				t.Errorf("did not expect problem at %s, got %s", path, problemsByPath[path])
			}
			continue
		}
		if problemsByPath[path] != code {
			t.Errorf("path %s: expected code %s, got %s", path, code, problemsByPath[path])
		}
	}

	// every clause (including the compatible ones) must be reported
	clauses := map[string]bool{}
	for _, r := range report.Requirements {
		clauses[r.Path] = true
	}
	for path := range expected {
		if !clauses[path] {
			t.Errorf("requirement for path %s not reported", path)
		}
	}
}

func TestPreflightQueryStringExpansion(t *testing.T) {
	idx := newPreflightTestIndex(t, "idx", map[string]*mapping.FieldMapping{
		"body": preflightFullTextField(),
	}, "")
	defer func() { _ = idx.Close() }()

	// references an unmapped field after query string expansion
	req := NewSearchRequest(NewQueryStringQuery("body:hello OR missing:world"))
	report, err := PreflightSearch(idx, req)
	if err != nil {
		t.Fatalf("preflight returned error: %v", err)
	}
	if report.Executable {
		t.Errorf("query string referencing unmapped field must not be executable")
	}
	found := false
	for _, p := range report.Indexes["idx"].Problems {
		if p.Code == PreflightReasonFieldNotMapped && p.Field == "missing" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected field_not_mapped problem for 'missing', got %v", report.Indexes["idx"].Problems)
	}
}

func TestPreflightDefaultSearchField(t *testing.T) {
	// no field on the clause and no default search field configured
	idx := newPreflightTestIndex(t, "idx", map[string]*mapping.FieldMapping{
		"body": preflightFullTextField(),
	}, "")
	defer func() { _ = idx.Close() }()

	report, err := PreflightSearch(idx, NewSearchRequest(NewTermQuery("value")))
	if err != nil {
		t.Fatalf("preflight returned error: %v", err)
	}
	if report.Executable {
		t.Errorf("term query without field and no default field must not be executable")
	}
	preflightFindProblem(t, report, "idx", "", PreflightReasonDefaultFieldMissing)

	// with a default search field configured, the clause resolves to it
	idxDefault := newPreflightTestIndex(t, "idx-default", map[string]*mapping.FieldMapping{
		"body": preflightFullTextField(),
	}, "body")
	defer func() { _ = idxDefault.Close() }()
	report, err = PreflightSearch(idxDefault, NewSearchRequest(NewTermQuery("value")))
	if err != nil {
		t.Fatalf("preflight returned error: %v", err)
	}
	if !report.Executable {
		t.Errorf("term query should resolve against the default search field, got %v",
			report.Indexes["idx-default"].Problems)
	}
}

func TestPreflightAliasMemberDifferences(t *testing.T) {
	good := newPreflightTestIndex(t, "good", map[string]*mapping.FieldMapping{
		"body": preflightFullTextField(),
	}, "")
	defer func() { _ = good.Close() }()
	bad := newPreflightTestIndex(t, "bad", map[string]*mapping.FieldMapping{
		"other": preflightFullTextField(),
	}, "")
	defer func() { _ = bad.Close() }()

	alias := NewIndexAlias(good, bad)
	alias.SetName("tenant-alias")

	tq := NewTermQuery("value")
	tq.SetField("body")
	report, err := PreflightSearch(alias, NewSearchRequest(tq))
	if err != nil {
		t.Fatalf("preflight returned error: %v", err)
	}
	if report.Executable {
		t.Errorf("alias with an incompatible member must not be executable")
	}
	if !report.Indexes["good"].OK {
		t.Errorf("good member should evaluate OK")
	}
	if report.Indexes["bad"].OK {
		t.Errorf("bad member should not evaluate OK")
	}
	preflightFindProblem(t, report, "bad", "", PreflightReasonFieldNotMapped)

	// nested aliases are flattened to leaf indexes
	nested := NewIndexAlias(alias, good)
	nested.SetName("nested")
	report, err = PreflightSearch(nested, NewSearchRequest(tq))
	if err != nil {
		t.Fatalf("preflight returned error: %v", err)
	}
	if len(report.Indexes) != 2 {
		t.Errorf("expected 2 leaf indexes, got %d (%v)", len(report.Indexes), report.Indexes)
	}
	if report.Executable {
		t.Errorf("nested alias with an incompatible leaf must not be executable")
	}

	// an alias over compatible members is executable
	aliasOK := NewIndexAlias(good)
	report, err = PreflightSearch(aliasOK, NewSearchRequest(tq))
	if err != nil {
		t.Fatalf("preflight returned error: %v", err)
	}
	if !report.Executable {
		t.Errorf("alias over compatible members must be executable")
	}
}

func TestPreflightSearchReusesJudgment(t *testing.T) {
	good := newPreflightTestIndex(t, "good", map[string]*mapping.FieldMapping{
		"body": preflightFullTextField(),
	}, "")
	defer func() { _ = good.Close() }()
	bad := newPreflightTestIndex(t, "bad", map[string]*mapping.FieldMapping{
		"other": preflightFullTextField(),
	}, "")
	defer func() { _ = bad.Close() }()

	if err := good.Index("1", map[string]interface{}{"body": "value"}); err != nil {
		t.Fatalf("error indexing document: %v", err)
	}

	tq := NewTermQuery("value")
	tq.SetField("body")
	req := NewSearchRequest(tq)
	alias := NewIndexAlias(good, bad)

	// SearchWithPreflight must not execute the search when the preflight fails
	sr, report, err := SearchWithPreflight(context.Background(), alias, req)
	if err == nil {
		t.Fatalf("expected preflight execution error")
	}
	if sr != nil {
		t.Errorf("expected nil search result when preflight fails")
	}
	if report == nil || report.Executable {
		t.Fatalf("expected non-executable report")
	}
	var execErr *PreflightExecutionError
	if !errors.As(err, &execErr) {
		t.Fatalf("expected *PreflightExecutionError, got %T", err)
	}

	// attaching the same report to the context makes the alias fail fast
	_, err = alias.SearchInContext(ContextWithPreflightReport(context.Background(), report), req)
	if !errors.As(err, &execErr) {
		t.Fatalf("expected enforced search to fail with *PreflightExecutionError, got %v", err)
	}

	// without the attached report the default search semantics are unchanged
	sr, err = alias.Search(req)
	if err != nil {
		t.Fatalf("default alias search must keep its semantics, got %v", err)
	}
	if sr.Total != 1 {
		t.Errorf("expected partial result from the good member, got total %d", sr.Total)
	}

	// with an executable report the actual search proceeds and reuses it
	goodReq := NewSearchRequest(tq)
	goodReport, err := PreflightSearch(good, goodReq)
	if err != nil {
		t.Fatalf("preflight returned error: %v", err)
	}
	if !goodReport.Executable {
		t.Fatalf("expected executable report for good index")
	}
	sr, err = good.SearchInContext(ContextWithPreflightReport(context.Background(), goodReport), goodReq)
	if err != nil {
		t.Fatalf("enforced executable search should proceed, got %v", err)
	}
	if sr.Total != 1 {
		t.Errorf("expected 1 match, got %d", sr.Total)
	}

	// a report built for another topology cannot be reused after a change
	foreign := NewIndexAlias(bad)
	_, err = foreign.SearchInContext(ContextWithPreflightReport(context.Background(), goodReport), NewSearchRequest(tq))
	if err != ErrorTwoPhaseSearchInconsistency {
		t.Fatalf("expected topology inconsistency error, got %v", err)
	}
}

func TestPreflightRequirementCoverage(t *testing.T) {
	idx := newPreflightTestIndex(t, "idx", map[string]*mapping.FieldMapping{
		"body": preflightFullTextField(),
	}, "")
	defer func() { _ = idx.Close() }()

	// match all / match none / doc id queries and score/id sorts need nothing
	req := NewSearchRequest(NewMatchAllQuery())
	report, err := PreflightSearch(idx, req)
	if err != nil {
		t.Fatalf("preflight returned error: %v", err)
	}
	if !report.Executable || len(report.Requirements) != 0 {
		t.Errorf("match all with score sort needs no requirements, got %+v", report)
	}

	docIDReq := NewSearchRequest(NewDocIDQuery([]string{"x"}))
	report, err = PreflightSearch(idx, docIDReq)
	if err != nil {
		t.Fatalf("preflight returned error: %v", err)
	}
	if !report.Executable || len(report.Requirements) != 0 {
		t.Errorf("doc id query needs no field requirements, got %+v", report.Requirements)
	}

	// geo distance sort requires both doc values and an indexed field
	geoSortReq := NewSearchRequest(NewMatchAllQuery())
	geoSortReq.Sort = search.SortOrder{&search.SortGeoDistance{Field: "body"}}
	report, err = PreflightSearch(idx, geoSortReq)
	if err != nil {
		t.Fatalf("preflight returned error: %v", err)
	}
	var found bool
	for _, r := range report.Requirements {
		if r.Clause == "sort_geo_distance" &&
			r.Needs&CapabilityDocValues != 0 && r.Needs&CapabilityIndexed != 0 {
			found = true
		}
	}
	if !found {
		t.Errorf("expected geo distance sort requirement, got %+v", report.Requirements)
	}
}

func TestPreflightInvalidInputs(t *testing.T) {
	if _, err := PreflightSearch(nil, NewSearchRequest(NewMatchAllQuery())); err == nil {
		t.Errorf("expected error for nil index")
	}
	idx := newPreflightTestIndex(t, "idx", map[string]*mapping.FieldMapping{
		"body": preflightFullTextField(),
	}, "")
	defer func() { _ = idx.Close() }()
	if _, err := PreflightSearch(idx, nil); err == nil {
		t.Errorf("expected error for nil request")
	}
	if _, err := PreflightSearch(idx, &SearchRequest{Query: nil}); err == nil {
		t.Errorf("expected error for nil query")
	}
	empty := NewIndexAlias()
	if _, err := PreflightSearch(empty, NewSearchRequest(NewMatchAllQuery())); err != ErrorAliasEmpty {
		t.Errorf("expected ErrorAliasEmpty, got %v", err)
	}
}

func TestPreflightScorchEnforcement(t *testing.T) {
	im := mapping.NewIndexMapping()
	im.DefaultMapping.Dynamic = false
	im.DefaultField = ""
	im.DefaultMapping.AddFieldMappingsAt("body", preflightFullTextField())
	im.DefaultMapping.AddFieldMappingsAt("title", preflightTextNoTermVectorsNoDocValues())

	path := t.TempDir()
	idx, err := NewUsing(path, im, "scorch", "scorch", nil)
	if err != nil {
		t.Fatalf("error creating scorch index: %v", err)
	}
	defer func() { _ = idx.Close() }()
	idx.SetName("scorch-idx")

	if err := idx.Index("1", map[string]interface{}{"body": "value"}); err != nil {
		t.Fatalf("error indexing document: %v", err)
	}

	goodReq := NewSearchRequest(func() *query.TermQuery {
		q := NewTermQuery("value")
		q.SetField("body")
		return q
	}())
	report, err := PreflightSearch(idx, goodReq)
	if err != nil {
		t.Fatalf("preflight returned error: %v", err)
	}
	sr, err := idx.SearchInContext(ContextWithPreflightReport(context.Background(), report), goodReq)
	if err != nil {
		t.Fatalf("enforced executable scorch search should proceed, got %v", err)
	}
	if sr.Total != 1 {
		t.Errorf("expected 1 match, got %d", sr.Total)
	}

	badReq := NewSearchRequest(NewPhraseQuery([]string{"a", "b"}, "title"))
	badReport, err := PreflightSearch(idx, badReq)
	if err != nil {
		t.Fatalf("preflight returned error: %v", err)
	}
	if badReport.Executable {
		t.Fatalf("expected non-executable report for phrase on title")
	}
	if _, err := idx.SearchInContext(ContextWithPreflightReport(context.Background(), badReport), badReq); err == nil {
		t.Fatalf("expected enforced scorch search to fail fast")
	}
}
